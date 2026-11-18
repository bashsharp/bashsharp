// Copyright (c) 2024, the sh authors.
// See LICENSE for licensing information.

package interp

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// syncBuffer is a goroutine-safe buffer collecting both stdout and stderr,
// so the relative order of a builtin's error output and a following echo is
// preserved the way the combined-stream spec cases expect.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestDeclare2Fidelity covers the bash-5.3 drop-in fidelity gaps closed in the
// declare/typeset/local/readonly/export/unset builtins. It mirrors the
// {input, want} table style of TestRunnerRun: `want` is the combined
// stdout+stderr, and a script whose final command exits non-zero appends the
// "exit status N" line that interp.Runner.Run reports as its error.
func TestDeclare2Fidelity(t *testing.T) {
	// Not t.Parallel: several cases pipe a builtin into an external `grep`
	// (`declare -pr | grep …`), so they fork subprocesses and depend on the
	// process fd table. Under CI's 2-core -race a concurrent test's exec can
	// truncate the piped output; run this fidelity suite serially.

	cases := []struct {
		in, want string
	}{
		// builtin-vars__032: subscript-unset of a declared-but-unset scalar.
		// A non-zero subscript is the "not an array variable" error case, but
		// subscript `[0]` (and any index that evaluates to 0, such as the
		// unset name in `["key"]`) aliases the scalar itself, so bash returns
		// success and merely no-ops.
		{
			"declare undef; unset -v 'undef[1]'; echo status=$?",
			"unset: undef: not an array variable\nstatus=1\n",
		},
		{
			`declare undef; unset -v 'undef["key"]'; echo status=$?`,
			"status=0\n",
		},
		{
			"declare undef; unset -v 'undef[0]'; echo status=$?",
			"status=0\n",
		},
		// The associative-array sibling rows from the same spec case still
		// behave: a missing key is a successful no-op.
		{
			`declare -A A=(['key']=val); unset -v 'A[1]'; echo $?; unset -v 'A["key"]'; echo $?`,
			"0\n0\n",
		},

		// assign-extended: eval -- "$(declare -p arr)" must restore a
		// sparse indexed array, including explicitly set empty elements.
		{
			`arr=(1 2 3); eval -- "$(arr=(); arr[3]=; arr[4]=foo; declare -p arr)"; for i in {0..4}; do echo "arr[$i]: ${arr[$i]+set ... [}${arr[$i]-unset}${arr[$i]+]}"; done`,
			"arr[0]: unset\narr[1]: unset\narr[2]: unset\narr[3]: set ... []\narr[4]: set ... [foo]\n",
		},
		// array-assign__008: arithmetic side effects in indexed
		// subscripts are preserved instead of being overwritten by the
		// final element assignment.
		{
			`echo assign=$(( z[0] = 42 )); a[a[0]=1]=X; declare -p a; a[ a[2]=3 ]=Y; declare -p a; echo ---; a[ a[0]+=1 ]+=X; declare -p a`,
			"assign=42\ndeclare -a a=([0]=\"1\" [1]=\"X\")\ndeclare -a a=([0]=\"1\" [1]=\"X\" [2]=\"3\" [3]=\"Y\")\n---\ndeclare -a a=([0]=\"2\" [1]=\"X\" [2]=\"3X\" [3]=\"Y\")\n",
		},
		// array-assign__010: declare rejects whitespace-bearing
		// subscripts, but the arithmetic subscript side effect remains.
		{
			`declare a[a[0]=1]=X; declare -p a; declare a[ a[2]=3 ]=Y; declare -p a`,
			"declare -a a=([0]=\"1\" [1]=\"X\")\ndeclare: `a[': not a valid identifier\ndeclare: `]=Y': not a valid identifier\ndeclare -a a=([0]=\"1\" [1]=\"X\" [2]=\"3\")\n",
		},
		// array__009 and array__077: an out-of-range negative
		// subscript reports an error but expands to an empty string.
		{
			`a=(1 '2 3'); printf '[%s]\n' "${a[-1]}" "${a[-2]}" "${a[-5]}"`,
			"a: bad array subscript\n[2 3]\n[1]\n[]\n",
		},
		{
			`a=(x); echo "[${a[-2]}]"; echo $?; echo "[$((a[-2]))]"; echo $?`,
			"a: bad array subscript\n[]\n0\na: bad array subscript\n[0]\n0\n",
		},
		// array-literal__010: compound-assignment RHS values expand
		// before mutations, while later explicit indexes see earlier
		// elements installed by the same literal.
		{
			`a=(old1 old2 old3); a=("${a[2]}" "${a[0]}" "${a[1]}" "${a[2]}" "${a[0]}"); declare -p a; a=(old1 old2 old3); old1=101 old2=102 old3=103; new1=201 new2=202 new3=203; a+=([0]=new1 [1]=new2 [2]=new3 [5]="${a[2]}" [a[0]]="${a[0]}" [a[1]]="${a[1]}"); declare -p a`,
			"declare -a a=([0]=\"old3\" [1]=\"old1\" [2]=\"old2\" [3]=\"old3\" [4]=\"old1\")\ndeclare -a a=([0]=\"new1\" [1]=\"new2\" [2]=\"new3\" [5]=\"old3\" [201]=\"old1\" [202]=\"old2\")\n",
		},

		// builtin-vars__026: a name that cannot even begin a variable
		// identifier (pure punctuation) is not a botched identifier; bare
		// `unset` silently falls through to the function namespace and exits 0.
		{
			"unset %; echo status=$?",
			"status=0\n",
		},
		{
			"unset @; echo status=$?",
			"status=0\n",
		},
		// Regression guard: digit/letter-led invalid names must still error.
		{
			"unset '1bad'; echo after=$?",
			"after=0\n",
		},
		// Regression guard: the set-scalar `[0]` whole-variable unset path.
		{
			"s=abc; unset 's[0]'; echo status:$?; echo ${s-unset}",
			"status:0\nunset\n",
		},

		// assign-extended__012: query-mode words with `=` are looked up
		// literally for diagnostics; they are not declarations.
		{
			"declare -p foo=bar; echo status=$?; a=b; declare -p a foo=bar; echo status=$?",
			"declare: foo=bar: not found\nstatus=1\ndeclare -- a=\"b\"\ndeclare: foo=bar: not found\nstatus=1\n",
		},
		// assign-extended__013: a no-name `declare -p` with an
		// attribute-selecting flag filters the full listing to the
		// variables carrying that attribute (`declare -pn` lists only
		// namerefs, `declare -pr` only readonly, `declare -px` only
		// exported).
		{
			"test_var1=111; readonly test_var2=222; export test_var3=333; declare -n test_var4=test_var1; echo '[declare -pn]'; declare -pn | grep -E '^declare -. test_var'; echo '[declare -pr]'; declare -pr | grep -E '^declare -. test_var'; echo '[declare -px]'; declare -px | grep -E '^declare -. test_var'",
			"[declare -pn]\ndeclare -n test_var4=\"test_var1\"\n[declare -pr]\ndeclare -r test_var2=\"222\"\n[declare -px]\ndeclare -x test_var3=\"333\"\n",
		},
		// assign-extended__004: with extdebug enabled, per-name
		// declare -F includes the function definition line and source.
		{
			"shopt -s extdebug\nadd() { :; }\ndeclare -F\necho\ndeclare -F add",
			"declare -f add\n\nadd 2 bash\n",
		},
		// assign-extended__010: explicit readonly/export -p operands print
		// nothing, but still mark existing local operands; local -p then
		// shows the merged -rx flags.
		{
			"f(){ local test_var5=555; readonly -p test_var5; export -p test_var5; local -p test_var5; }; f",
			"declare -rx test_var5=\"555\"\n",
		},
		// assign-extended__015: once explicit names are supplied, bash 5.3
		// ignores the -n/-r/-x attribute flags for selection and prints
		// every named variable that exists, with a "not found" diagnostic
		// (exit 1) for the names that do not.
		{
			"test_var1=111; readonly test_var2=222; export test_var3=333; declare -n test_var4=test_var1; echo '[declare -pn]'; declare -pn test_var{0..5}; echo '[declare -pr]'; declare -pr test_var{0..5}; echo '[declare -px]'; declare -px test_var{0..5}",
			"[declare -pn]\ndeclare: test_var0: not found\ndeclare -- test_var1=\"111\"\ndeclare -r test_var2=\"222\"\ndeclare -x test_var3=\"333\"\ndeclare -n test_var4=\"test_var1\"\ndeclare: test_var5: not found\n[declare -pr]\ndeclare: test_var0: not found\ndeclare -- test_var1=\"111\"\ndeclare -r test_var2=\"222\"\ndeclare -x test_var3=\"333\"\ndeclare -n test_var4=\"test_var1\"\ndeclare: test_var5: not found\n[declare -px]\ndeclare: test_var0: not found\ndeclare -- test_var1=\"111\"\ndeclare -r test_var2=\"222\"\ndeclare -x test_var3=\"333\"\ndeclare -n test_var4=\"test_var1\"\ndeclare: test_var5: not found\nexit status 1",
		},
		// assign__045: readonly -a/-A on a fresh name sets readonly only;
		// it must not create an empty array. Existing arrays are preserved.
		{
			"declare -a arr1; readonly -a arr2; declare -A dict1; readonly -A dict2; declare -p arr1 arr2 dict1 dict2",
			"declare -a arr1\ndeclare -r arr2\ndeclare -A dict1\ndeclare -r dict2\n",
		},
		// assign-extended__035: `typeset +r r=value` on a readonly var
		// reports the failed readonly mutation once and leaves it unchanged.
		{
			"readonly r=r1; echo r=$r; typeset +r r=r2; echo r=$r",
			"r=r1\ntypeset: r: readonly variable\nr=r1\n",
		},
		// assign-extended__007/__008: no-arg declare/export/readonly
		// list variables instead of falling through to an empty result.
		{
			"zz_assign_plain=111; readonly zz_assign_ro=222; export zz_assign_ex=333; declare | grep '^zz_assign_'; readonly | grep '^declare -r zz_assign_ro='; export | grep '^declare -x zz_assign_ex='",
			"zz_assign_ex=333\nzz_assign_plain=111\nzz_assign_ro=222\ndeclare -r zz_assign_ro=\"222\"\ndeclare -x zz_assign_ex=\"333\"\n",
		},
		// assign__014: a command-prefix assignment for readonly is
		// visible to command substitutions, but unrelated prefix vars
		// do not remain as shell variables for later RHS expansion.
		{
			"FOO=foo readonly v=$(missing_assign014 FOO); echo v=$v; FOO=foo readonly v2=$FOO; echo v2=$v2",
			"\"missing_assign014\": executable file not found in $PATH\nv=\nv2=\n",
		},
		// assign__025: unsetting a function call's temporary binding
		// uncovers the pre-call global, not an empty value.
		{
			"f(){ echo z=$z; z=mutated-temp; echo z=$z; unset z; echo z=$z; }; z=global; z=temp-binding f; echo z=$z",
			"z=temp-binding\nz=mutated-temp\nz=global\nz=global\n",
		},
		// assign__029: alias-expanded declaration builtins must keep
		// following name=value words as declaration operands, not execute
		// bare `export`/`readonly` and treat the words as lost suffixes.
		{
			"shopt -s expand_aliases\nwords='a b c'\nalias e=export\nalias r=readonly\ne ex=$words\nr ro=$words\nprintf '[%q, %q]\\n' \"$ex\" \"$ro\"",
			"[a\\ b\\ c, a\\ b\\ c]\n",
		},
		// assign__038/__040: command substitutions in declaration and
		// bare-assignment RHS words run before a trailing stderr
		// redirect is applied.
		{
			"readonly r=$(missing_assign038) 2>/dev/null\necho done\nb=$(missing_assign040) 2>/dev/null\necho after",
			"\"missing_assign038\": executable file not found in $PATH\ndone\n\"missing_assign040\": executable file not found in $PATH\nafter\n",
		},
	}

	p := syntax.NewParser()
	for _, c := range cases {
		t.Run("", func(t *testing.T) {
			t.Logf("input: %q", c.in)
			file, err := p.Parse(strings.NewReader(c.in), "")
			if err != nil {
				t.Fatalf("parse: %v", err)
			}

			var buf syncBuffer
			r, err := New(Dir(t.TempDir()), StdIO(nil, &buf, &buf))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if rerr := r.Run(ctx, file); rerr != nil {
				buf.Write([]byte(rerr.Error()))
			}
			if got := buf.String(); got != c.want {
				t.Fatalf("wrong output in %q:\nwant: %q\ngot:  %q", c.in, c.want, got)
			}
		})
	}
}
