// Copyright (c) 2017, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package interp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestArrayElementUnsetAndSetnessFidelity(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "assoc_nameref_element_target",
			in: "show-values() { echo values: ${A[@]}; }\n" +
				"declare -A A=(['K']=val)\n" +
				"show-values\n" +
				"declare -n ref='A[\"K\"]'\n" +
				"echo before $ref\n" +
				"ref='val2'\n" +
				"echo after $ref\n" +
				"show-values\n" +
				"echo ---\n" +
				"key=K\n" +
				"declare -n ref='A[$key]'\n" +
				"echo before $ref\n" +
				"ref='val3'\n" +
				"echo after $ref\n" +
				"show-values\n",
			want: "values: val\nbefore val\nafter val2\nvalues: val2\n---\nbefore val2\nafter val3\nvalues: val3\n",
		},
		{
			name: "assoc_indirect_and_printf_element_target",
			in: "show-values() { echo values: ${A[@]}; }\n" +
				"declare -A A=(['K']=val)\n" +
				"show-values\n" +
				"declare ref='A[\"K\"]'\n" +
				"echo ref ${!ref}\n" +
				"key=K\n" +
				"declare ref='A[$key]'\n" +
				"echo ref ${!ref}\n" +
				"printf -v 'A[$key]' '/%s/' val2\n" +
				"show-values\n",
			want: "values: val\nref val\nref val\nvalues: /val2/\n",
		},
		{
			name: "assoc_unset_element_delete",
			in: "show-len() { echo len=${#assoc[@]}; }\n" +
				"declare -A assoc=(['K']=val)\n" +
				"show-len\n" +
				"unset -v 'assoc[\"K\"]'\n" +
				"show-len\n" +
				"declare -A assoc=(['K']=val)\n" +
				"key=K\n" +
				"unset -v 'assoc[$key]'\n" +
				"show-len\n",
			want: "len=1\nlen=0\nlen=0\n",
		},
		{
			name: "element_setness",
			in: "typeset -A assoc\n" +
				"assoc=([empty]='' [k]=v)\n" +
				"[[ -v assoc[empty] ]]; echo empty=$?\n" +
				"[[ -v assoc[k] ]]; echo k=$?\n" +
				"[[ -v assoc[nonexistent] ]]; echo nonexistent=$?\n" +
				"array=(1 2 3 '')\n" +
				"test -v 'array[1+1]'; echo status=$?\n" +
				"test -v 'array[4+1]'; echo status=$?\n" +
				"typeset -a sparse\n" +
				"sparse=('' nonempty)\n" +
				"zero=0\n" +
				"[[ -v sparse[zero+0] ]]; echo zero=$?\n" +
				"[[ -v sparse[zero+1] ]]; echo one=$?\n" +
				"[[ -v sparse[zero+2] ]]; echo two=$?\n",
			want: "empty=0\nk=0\nnonexistent=1\nstatus=0\nstatus=1\nzero=0\none=0\ntwo=1\n",
		},
		{
			name: "empty_array_element_default",
			in: "argv.py() { python3 - \"$@\" <<'PY'\n" +
				"import sys\n" +
				"print(sys.argv[1:])\n" +
				"PY\n" +
				"}\n" +
				"single=('')\n" +
				"argv.py ${single[@]:-none} x \"${single[@]:-none}\"\n",
			want: "['none', 'x', 'none']\n",
		},
		{
			name: "assoc_unquoted_keys_split",
			in: "argv.py() { python3 - \"$@\" <<'PY'\n" +
				"import sys\n" +
				"print(sys.argv[1:])\n" +
				"PY\n" +
				"}\n" +
				"declare -A A\n" +
				"A['X X']='xx'\n" +
				"A['Y Y']='yy'\n" +
				"argv.py \"${A[*]}\"\n" +
				"argv.py \"${!A[*]}\"\n" +
				"argv.py ${A[@]}\n" +
				"argv.py ${!A[@]}\n",
			want: "['xx yy']\n['X X Y Y']\n['xx', 'yy']\n['X', 'X', 'Y', 'Y']\n",
		},
		{
			name: "assoc_slice_values",
			in: "argv.py() { python3 - \"$@\" <<'PY'\n" +
				"import sys\n" +
				"print(sys.argv[1:])\n" +
				"PY\n" +
				"}\n" +
				"declare -A a\n" +
				"a[xx]=1\n" +
				"a[yy]=2\n" +
				"a[zz]=3\n" +
				"a[aa]=4\n" +
				"a[bb]=5\n" +
				"argv.py ${a[@]: 0: 3}\n" +
				"argv.py ${a[@]: 1: 3}\n" +
				"argv.py ${a[@]: 2: 3}\n" +
				"argv.py ${a[@]: 3: 3}\n" +
				"argv.py ${a[@]: 4: 3}\n" +
				"argv.py ${a[@]: 5: 3}\n",
			want: "['4', '1', '3']\n['4', '1', '3']\n['1', '3', '5']\n['3', '5', '2']\n['5', '2']\n['2']\n",
		},
		{
			name: "assoc_literal_append_residue",
			in: "argv.py() { python3 - \"$@\" <<'PY'\n" +
				"import sys\n" +
				"print(sys.argv[1:])\n" +
				"PY\n" +
				"}\n" +
				"declare -A a\n" +
				"hello=100\n" +
				"a=([hello]=1 [hello]+=2)\n" +
				"printf 'keys: '; argv.py \"${!a[@]}\"\n" +
				"printf 'vals: '; argv.py \"${a[@]}\"\n" +
				"a+=([hello]+=:34 [hello]+=:56)\n" +
				"printf 'keys: '; argv.py \"${!a[@]}\"\n" +
				"printf 'vals: '; argv.py \"${a[@]}\"\n",
			want: "keys: ['hello']\nvals: ['2']\nkeys: ['hello']\nvals: ['2:34:56']\n",
		},
		{
			name: "indexed_subscript_indirect",
			in: "argv.py() { python3 - \"$@\" <<'PY'\n" +
				"import sys\n" +
				"print(sys.argv[1:])\n" +
				"PY\n" +
				"}\n" +
				"foo=bar\n" +
				"a=('1 2' foo '2 3')\n" +
				"argv.py \"${!a[1]}\"\n",
			want: "['bar']\n",
		},
		{
			name: "indexed_literal_subscripts_see_prior_elements",
			in: "argv.py() { python3 - \"$@\" <<'PY'\n" +
				"import sys\n" +
				"print(sys.argv[1:])\n" +
				"PY\n" +
				"}\n" +
				"a=([0]=1+2+3 [a[0]]=10 [a[6]]=hello)\n" +
				"printf 'keys: '; argv.py \"${!a[@]}\"\n" +
				"printf 'vals: '; argv.py \"${a[@]}\"\n",
			want: "keys: ['0', '6', '10']\nvals: ['1+2+3', '10', 'hello']\n",
		},
		{
			name: "indexed_literal_explicit_sparse_indices",
			in:   "a=([0]=1+2+3 [6]=10 [10]=hello)\ndeclare -p a\n",
			want: "declare -a a=([0]=\"1+2+3\" [6]=\"10\" [10]=\"hello\")\n",
		},
		{
			name: "shopt_long_set_strict_array",
			// Oils uses OSH-only `shopt --set strict_array` behind
			// a `$SH` guard. The fidelity harness runs our binary as
			// `/ours`, so accept that one OSH probe as a no-op while
			// preserving bash-style rejection for other long shopt
			// forms.
			in: "SH=/ours\n" +
				"case $SH in bash) ;; *) shopt --set strict_array ;; esac\n" +
				"s1=hello\n" +
				"s2=world\n" +
				"eval 's1=(1 2 3 4)'\n" +
				"echo status=$?\n" +
				"declare -p s1\n" +
				"eval 's2+=(1 2 3 4)'\n" +
				"echo status=$?\n" +
				"declare -p s2\n",
			want: "status=0\ndeclare -a s1=([0]=\"1\" [1]=\"2\" [2]=\"3\" [3]=\"4\")\nstatus=0\ndeclare -a s2=([0]=\"world\" [1]=\"1\" [2]=\"2\" [3]=\"3\" [4]=\"4\")\n",
		},
		{
			name: "array_prefix_assignment_env_string",
			in:   "A=a B=(b b) true\n" + "echo status=$?\n",
			want: "status=0\n",
		},
		{
			name: "negative_array_slice_status",
			in: "a=(1 2 3 4 5)\n" +
				"echo \"${a[@]: 1: -3}\"\n" +
				"echo status=$?\n",
			want: "-3: substring expression < 0\nstatus=0\n",
		},
		{
			name: "test_v_negative_empty_array",
			in:   "e=()\n[[ -v e[-1] ]] && echo yes\n",
			want: "e: bad array subscript\nexit status 1",
		},
		{
			name: "integer_indexed_element_append",
			in: "declare -ai iarr=(1 2 3)\n" +
				"iarr[0]+=2\n" +
				"declare -p iarr\n" +
				"iarr+=(4 5 6)\n" +
				"declare -p iarr\n",
			want: "declare -ai iarr=([0]=\"3\" [1]=\"2\" [2]=\"3\")\n" +
				"declare -ai iarr=([0]=\"3\" [1]=\"2\" [2]=\"3\" [3]=\"4\" [4]=\"5\" [5]=\"6\")\n",
		},
		{
			name: "array16_sparse_length_test",
			in: "unset -v a\n" +
				"a[1]=set\n" +
				"[[ -v a ]] ; echo $?\n" +
				"[[ -v a[1] ]] ; echo $?\n" +
				"[[ -v a[@] ]] ; echo $?\n" +
				"[[ ${#a[@]} > 0 ]]; echo $?\n",
			want: "1\n0\n0\n0\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			file, err := syntax.NewParser().Parse(strings.NewReader(tc.in), "")
			if err != nil {
				t.Fatalf("parse error: %v", err)
			}

			var cb lockedBuffer
			r, err := New(StdIO(nil, &cb, &cb))
			if err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := r.Run(ctx, file); err != nil {
				cb.Write([]byte(err.Error()))
			}

			if got := cb.String(); got != tc.want {
				t.Fatalf("wrong output in %q:\nwant: %q\ngot:  %q", tc.in, tc.want, got)
			}
		})
	}
}

// TestArraySubscriptSideEffectOnceFidelity covers array10.sub from bash's
// suite: a side-effecting subscript (`$((count++))` / `count++`) combined with
// a suffix word operator (`,,`, `/`, `:N`, `#?`) must evaluate the index only
// once. A stray paramIsSetNonColon/arrayElemSet call used to re-evaluate it,
// doubling the post-increment.
func TestArraySubscriptSideEffectOnceFidelity(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "case_mod_post_increment",
			in: "days=({Mon,Tues,Wednes,Thurs,Fri,Satur,Sun}day)\n" +
				"count=0\n" +
				"echo \"${days[${count}],,}, ${days[$((count++))],,}, ${days[$((count++))],,}\"\n",
			want: "monday, monday, tuesday\n",
		},
		{
			name: "prefix_removal_post_increment",
			in: "days=({Mon,Tues,Wednes,Thurs,Fri,Satur,Sun}day)\n" +
				"count=0\n" +
				"echo ${days[$((count++))]#?}\n" +
				"echo ${days[$((count++))]#?}\n" +
				"echo ${days[$((count++))]#?}\n",
			want: "onday\nuesday\nednesday\n",
		},
		{
			name: "substring_bare_increment",
			in: "days=({Mon,Tues,Wednes,Thurs,Fri,Satur,Sun}day)\n" +
				"count=0\n" +
				"echo ${days[count++]:2}\n" +
				"echo ${days[count++]:2}\n" +
				"echo ${days[count++]:2}\n",
			want: "nday\nesday\ndnesday\n",
		},
		{
			name: "colon_default_post_increment",
			in: "a=(x y z)\n" +
				"i=0\n" +
				"echo ${a[i++]:-foo}\n" +
				"echo i=$i\n",
			want: "x\ni=1\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			file, err := syntax.NewParser().Parse(strings.NewReader(tc.in), "")
			if err != nil {
				t.Fatalf("parse error: %v", err)
			}

			var cb lockedBuffer
			r, err := New(StdIO(nil, &cb, &cb))
			if err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := r.Run(ctx, file); err != nil {
				cb.Write([]byte(err.Error()))
			}

			if got := cb.String(); got != tc.want {
				t.Fatalf("wrong output in %q:\nwant: %q\ngot:  %q", tc.in, tc.want, got)
			}
		})
	}
}

// TestCompoundArraySubscriptScopeFidelity covers array29.sub: a `local -a
// foo=([0]=v)` element assignment must not leak an empty `foo` into the global
// scope (a later `local -A foo=(…)` in another function then wrongly tripped
// "cannot convert indexed to associative array"). It also pins the bash rule
// that a plain (non-append) compound assignment evaluates explicit subscript
// arithmetic against the array's pre-assignment value, while an append makes
// earlier elements visible.
func TestCompoundArraySubscriptScopeFidelity(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			// `local -a foo=([0]=v)` in pv2 must not leave a global `foo`
			// behind; otherwise pv3's `local -A foo` sees a stray indexed
			// global and refuses the associative declaration.
			name: "local_indexed_subscript_no_global_leak",
			in: "pv2() { local -a foo=( [0]=hello ); declare -p foo; }\n" +
				"pv2\n" +
				"pv3() { local -A foo=( v world ); declare -p foo; }\n" +
				"pv3\n" +
				"if declare -p foo >/dev/null 2>&1; then echo LEAKED; else echo clean; fi\n",
			want: "declare -a foo=([0]=\"hello\")\n" +
				"declare -A foo=([v]=\"world\" )\n" +
				"clean\n",
		},
		{
			name: "plain_assign_subscript_sees_prior_literal_element",
			in: "a=([0]=7)\n" +
				"a=([0]=10 [a[0]+5]=99)\n" +
				"declare -p a\n",
			want: "declare -a a=([0]=\"10\" [15]=\"99\")\n",
		},
		{
			name: "plain_assign_unset_subscript_sees_prior_literal_element",
			in: "unset a\n" +
				"a=([0]=10 [a[0]+5]=99)\n" +
				"declare -p a\n",
			want: "declare -a a=([0]=\"10\" [15]=\"99\")\n",
		},
		{
			// array33.sub: an explicit `declare -a` literal with a numeric
			// subscript on an associative variable must still trip the
			// conversion error. Premature publishing of the partial indexed
			// array used to overwrite prev's kind before the check, silently
			// converting it.
			name: "declare_indexed_literal_on_assoc_rejected",
			in: "declare -A A=([1]=1)\n" +
				"declare -a A=([2]=2)\n" +
				"declare -p A\n",
			want: "A: cannot convert associative to indexed array\n" +
				"declare -A A=([1]=\"1\" )\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			file, err := syntax.NewParser().Parse(strings.NewReader(tc.in), "")
			if err != nil {
				t.Fatalf("parse error: %v", err)
			}

			var cb lockedBuffer
			r, err := New(StdIO(nil, &cb, &cb))
			if err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := r.Run(ctx, file); err != nil {
				cb.Write([]byte(err.Error()))
			}

			if got := cb.String(); got != tc.want {
				t.Fatalf("wrong output in %q:\nwant: %q\ngot:  %q", tc.in, tc.want, got)
			}
		})
	}
}

// TestArrayExpandOnceIndexedSubscriptsFidelity covers array32.sub from bash's
// suite: with array_expand_once, an indexed subscript that expands once to a
// literal command substitution is an arithmetic operand error and must not run
// the embedded command.
func TestArrayExpandOnceIndexedSubscriptsFidelity(t *testing.T) {
	t.Parallel()

	in := "export subscript='$(echo INJECTION! >&2 ; echo 0)'\n" +
		"shopt -s array_expand_once\n" +
		"printf -v a[\"$subscript\"] %s hi\n" +
		"declare -p a\n" +
		"unset a\n" +
		"printf -v \"a[$subscript]\" %s hi\n" +
		"declare -p a\n" +
		"unset -v a\n" +
		"read a[\"$subscript\"] <<<hi\n" +
		"declare -p a\n" +
		"declare -a a\n" +
		"read a[\"$subscript\"] <<<hi\n" +
		"declare -p a\n" +
		"unset -v a\n" +
		"declare -a a\n" +
		"declare -i a[\"$subscript\"]=42\n" +
		"declare -p a\n" +
		"unset -v a\n" +
		"a=( [$subscript]=hi )\n" +
		"declare -p a\n" +
		"declare -a a\n" +
		"a=( [$subscript]=hi )\n" +
		"declare -p a\n"
	want := "arithmetic syntax error: operand expected (error token is \"$(echo INJECTION! >&2 ; echo 0)\")\n" +
		"declare: a: not found\n" +
		"arithmetic syntax error: operand expected (error token is \"$(echo INJECTION! >&2 ; echo 0)\")\n" +
		"declare: a: not found\n" +
		"arithmetic syntax error: operand expected (error token is \"$(echo INJECTION! >&2 ; echo 0)\")\n" +
		"declare: a: not found\n" +
		"arithmetic syntax error: operand expected (error token is \"$(echo INJECTION! >&2 ; echo 0)\")\n" +
		"declare -a a\n" +
		"arithmetic syntax error: operand expected (error token is \"$(echo INJECTION! >&2 ; echo 0)\")\n" +
		"declare -ai a\n" +
		"arithmetic syntax error: operand expected (error token is \"$(echo INJECTION! >&2 ; echo 0)\")\n" +
		"declare -a a=()\n" +
		"arithmetic syntax error: operand expected (error token is \"$(echo INJECTION! >&2 ; echo 0)\")\n" +
		"declare -a a=()\n"

	file, err := syntax.NewParser().Parse(strings.NewReader(in), "./array32.sub")
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	var cb lockedBuffer
	r, err := New(StdIO(nil, &cb, &cb))
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := r.Run(ctx, file); err != nil {
		cb.Write([]byte(err.Error()))
	}

	if got := cb.String(); got != want {
		t.Fatalf("wrong output:\nwant: %q\ngot:  %q", want, got)
	}
}
