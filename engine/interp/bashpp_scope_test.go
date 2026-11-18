// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Every test in this file drives the interpreter FROM SOURCE.
//
// That is the point of the file rather than an incidental style. A scope bug
// is a bug about where the parser's node lands, when the runner pushes a
// block, and which of the runner's several read paths the value comes back
// out of — and a test which builds a *syntax.BashPPDecl by hand and calls
// Runner.bashPPDeclare directly cannot fail on any of those three. Only the
// untyped `var x = 1` / `const K = 2` shape is claimed by the parser today
// (sh/syntax/bashpp_decl.go), so that shape, placed inside real shell
// compound commands, is what exercises the block structure here.

func bashPPParse(t *testing.T, src string) *syntax.File {
	t.Helper()
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "bashpp_scope_test")
	if err != nil {
		t.Fatalf("parse %q: %v", src, err)
	}
	return f
}

// bashPPRun runs src in the bash++ dialect and returns its combined output.
// Combined, because a scope failure most often shows up as a diagnostic the
// script did not expect, and a test which only read stdout would report it as
// a missing line rather than as the error it is.
func bashPPRun(t *testing.T, src string, opts ...RunnerOption) string {
	t.Helper()
	var out strings.Builder
	opts = append([]RunnerOption{Lang(syntax.LangBashPP), StdIO(nil, &out, &out)}, opts...)
	r, err := New(opts...)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), bashPPParse(t, src)); err != nil {
		t.Fatalf("run %q: %v\noutput:\n%s", src, err, out.String())
	}
	return out.String()
}

func wantOutput(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Fatalf("output:\n%s\nwant:\n%s", got, want)
	}
}

// A subshell gets a private copy of the typed bindings, in every form the
// shell spells one: `( … )`, `&`, a pipeline element, and a process
// substitution. Each writes to a binding declared in the parent, and none of
// the writes may be observable afterwards.
//
// This matters more here than it does for a shell variable. Subshells in this
// interpreter are goroutines rather than fork(), so "the parent cannot see it"
// is not something the operating system guarantees for free — it is whatever
// [Runner.subshell] copies, and a shared cell pointer would make the parent
// see the write AND make the race detector right about it.
func TestBashPPSubshellsIsolateTypedBindings(t *testing.T) {
	t.Parallel()

	t.Run("explicit subshell", func(t *testing.T) {
		t.Parallel()
		got := bashPPRun(t, `
var x = 1
( x=99; var inner = 5; echo "in=$x inner=$inner" )
echo "out=$x inner=${inner-unset}"
`)
		wantOutput(t, got, "in=99 inner=5\nout=1 inner=unset\n")
	})

	t.Run("background subshell", func(t *testing.T) {
		t.Parallel()
		got := bashPPRun(t, `
var x = 1
( x=99 ) &
wait
echo "out=$x"
`)
		wantOutput(t, got, "out=1\n")
	})

	t.Run("pipeline element", func(t *testing.T) {
		t.Parallel()
		got := bashPPRun(t, `
var x = 1
true | { x=7; echo "in=$x"; }
echo "out=$x"
`)
		wantOutput(t, got, "in=7\nout=1\n")
	})

	t.Run("process substitution", func(t *testing.T) {
		t.Parallel()
		if _, err := os.Stat("/dev/fd"); err != nil {
			t.Skipf("no /dev/fd on this platform: %v", err)
		}
		got := bashPPRun(t, `
var x = 1
cat <(x=42; echo "in=$x")
echo "out=$x"
`)
		wantOutput(t, got, "in=42\nout=1\n")
	})
}

// The concurrency case the copy exists for, shaped so that the race detector
// has something to catch if the copy ever regresses to a shared pointer.
//
// Every background subshell writes the binding it inherited AND declares its
// own, while the parent keeps reading and writing the same names, so a shared
// cell or a shared entry map is touched from two goroutines without
// synchronisation. Run this package with -race; without the copy the map write
// alone is a hard runtime throw, not merely a report.
func TestBashPPConcurrentSubshellsDoNotRace(t *testing.T) {
	t.Parallel()
	got := bashPPRun(t, `
var shared = 1
capture() { echo "cap=$shared"; }
for i in 1 2 3 4 5 6 7 8; do
	( shared=99; var mine = 2; mine=$((mine + 1)); capture > /dev/null ) &
done
for i in 1 2 3 4 5 6 7 8; do
	shared=$((shared + 1))
done
wait
echo "shared=$shared"
`)
	// 1 + 8 increments in the parent; none of the eight `shared=99` writes
	// crosses back.
	wantOutput(t, got, "shared=9\n")
}

// A binding must have ONE value, whichever door the reader comes in by. The
// paths are enumerated rather than sampled because they are separately
// implemented — expansion goes through lookupVar, an exec handler is handed an
// overlay over the runner's environment, and an external command is handed a
// flattened string slice — and each was capable of answering from the shadowed
// shell variable instead.
func TestBashPPTypedBindingIsVisibleOnEveryPath(t *testing.T) {
	t.Parallel()

	t.Run("expansion", func(t *testing.T) {
		t.Parallel()
		got := bashPPRun(t, `
shadow=outer
var shadow = 7
echo "direct=$shadow default=${shadow-fallback} len=${#shadow}"
`)
		// `${shadow-fallback}` yielding the value rather than the fallback is
		// the assertion that a binding is SET, not merely present: a variable
		// whose Set flag is false takes the default here, and would also be
		// scrubbed out of a child's environment by execEnv as an unset name.
		wantOutput(t, got, "direct=7 default=7 len=1\n")
	})

	t.Run("name enumeration", func(t *testing.T) {
		t.Parallel()
		got := bashPPRun(t, `
shadow=outer
var shadow = 7
var solo = 8
for name in ${!shadow@} ${!solo@}; do echo "name=$name"; done
`)
		// `solo` exists ONLY as a typed binding, so a prefix expansion that
		// enumerated the shell's variables alone would not find it — and a
		// name a script can read with $solo but cannot discover with ${!s@}
		// is the same binding answering two ways.
		wantOutput(t, got, "name=shadow\nname=solo\n")
	})

	t.Run("HandlerCtx.Env", func(t *testing.T) {
		t.Parallel()
		var seen expand.Variable
		var occurrences int
		mw := func(next ExecHandlerFunc) ExecHandlerFunc {
			return func(ctx context.Context, args []string) error {
				if args[0] == "probe" {
					env := HandlerCtx(ctx).Env
					seen = env.Get("shadow")
					for name := range env.Each {
						if name == "shadow" {
							occurrences++
						}
					}
					return nil
				}
				return next(ctx, args)
			}
		}
		bashPPRun(t, "shadow=outer\nexport shadow\nvar shadow = 7\nprobe\n", ExecHandlers(mw))
		if seen.String() != "7" {
			t.Fatalf("HandlerCtx.Env.Get(shadow) = %q, want %q; a handler read the shadowed shell value",
				seen.String(), "7")
		}
		// Each must not report the name twice. A consumer flattening Each into
		// a child's environment would otherwise emit both values and leave the
		// winner to whichever end the reader scans from.
		if occurrences != 1 {
			t.Fatalf("HandlerCtx.Env.Each yielded shadow %d times, want exactly 1", occurrences)
		}
	})

	t.Run("external command", func(t *testing.T) {
		t.Parallel()
		const envBin = "/usr/bin/env"
		if _, err := os.Stat(envBin); err != nil {
			t.Skipf("no %s on this platform: %v", envBin, err)
		}
		got := bashPPRun(t, `
export shadow=outer
var shadow = 7
var unexported = 3
`+envBin+`
`)
		if !strings.Contains(got, "shadow=7\n") {
			t.Fatalf("the child's environment lacks shadow=7:\n%s", got)
		}
		if strings.Contains(got, "shadow=outer") {
			t.Fatalf("the child's environment still carries the shadowed value:\n%s", got)
		}
		// A Go declaration is a local. It inherits an export it shadows so the
		// child cannot disagree with the script, but it does not create one.
		if strings.Contains(got, "unexported=") {
			t.Fatalf("an unexported typed binding crossed execve:\n%s", got)
		}
	})

	t.Run("write path", func(t *testing.T) {
		t.Parallel()
		got := bashPPRun(t, `
shadow=outer
var shadow = 7
shadow=8
echo "after=$shadow"
( echo "child=$shadow" )
`)
		// An ordinary assignment writes THROUGH to the binding rather than
		// creating a shell variable beside it, so both paths still agree.
		wantOutput(t, got, "after=8\nchild=8\n")
	})

	t.Run("delete path", func(t *testing.T) {
		t.Parallel()
		got := bashPPRun(t, `
var x = 1
const K = 2
unset x
unset K
echo "x=$x K=$K"
`)
		if !strings.Contains(got, "cannot unset a bash++ var declaration") {
			t.Fatalf("unset of a var declaration was not refused:\n%s", got)
		}
		// A const is refused one layer earlier, by the shell's own readonly
		// check in the unset builtin — which is the point of `const` marking
		// the variable readonly rather than inventing a parallel notion of
		// immutability. The wording is bash's, and that is the right wording.
		if !strings.Contains(got, "K: cannot unset: readonly variable") {
			t.Fatalf("unset of a const declaration was not refused as readonly:\n%s", got)
		}
		if !strings.Contains(got, "x=1 K=2\n") {
			t.Fatalf("a refused unset still changed the bindings:\n%s", got)
		}
	})

	t.Run("const refuses assignment", func(t *testing.T) {
		t.Parallel()
		got := bashPPRun(t, "const K = 2\nK=5\necho \"K=$K\"\n")
		if !strings.Contains(got, "cannot assign to const") {
			t.Fatalf("assigning to a const was not refused:\n%s", got)
		}
		if !strings.Contains(got, "K=2\n") {
			t.Fatalf("a refused assignment still changed the binding:\n%s", got)
		}
	})
}

// Go's rule is that an identifier's scope begins at its declaration, so a
// function body cannot refer to a name declared after it. Getting this wrong
// is the natural consequence of capturing a POINTER to the enclosing block:
// the later declaration lands in the very map the capture points at, and the
// function starts seeing a name that did not exist where it was written.
func TestBashPPFunctionDoesNotSeeLaterDeclaration(t *testing.T) {
	t.Parallel()
	got := bashPPRun(t, `
before() { echo "before=${x-unset}"; }
var x = 1
after() { echo "after=${x-unset}"; }
before
after
`)
	wantOutput(t, got, "before=unset\nafter=1\n")
}

// The other half of the same rule: a capture freezes which NAMES are visible,
// not which VALUES they hold. A closure must observe later writes to what it
// closed over, which is why the snapshot shares cells.
func TestBashPPFunctionObservesLaterWritesToCapturedBinding(t *testing.T) {
	t.Parallel()
	got := bashPPRun(t, `
var x = 1
peek() { echo "peek=$x"; }
peek
x=42
peek
`)
	wantOutput(t, got, "peek=1\npeek=42\n")
}

// Funcs survive [Runner.Reset] — bash treats a function defined at
// construction as initial shell state, not per-Run scratch. A function that
// survived without its capture would resolve its free identifiers against
// whatever the next program happens to declare, which is exactly the dynamic
// binding the lexical scope exists to prevent.
func TestBashPPFunctionCaptureSurvivesReset(t *testing.T) {
	t.Parallel()
	var out strings.Builder
	r, err := New(Lang(syntax.LangBashPP), StdIO(nil, &out, &out))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := r.Run(ctx, bashPPParse(t, "var x = 1\nkeep() { echo \"keep=$x\"; }\nkeep\n")); err != nil {
		t.Fatal(err)
	}
	r.Reset()
	// A fresh program: its own outermost block, its own declaration of the
	// same name. The surviving function must still answer from what it
	// captured, not from the new binding.
	if err := r.Run(ctx, bashPPParse(t, "keep\nvar x = 9\nkeep\necho \"now=$x\"\n")); err != nil {
		t.Fatal(err)
	}
	wantOutput(t, out.String(), "keep=1\nkeep=1\nkeep=1\nnow=9\n")
}

// A loop body is a block, so each iteration declares into a scope of its own.
// Two things fall out of that and both are asserted, because an implementation
// can get one without the other: the body may redeclare the same identifiers
// on the next pass (a shared scope would report a redeclaration), and a
// closure defined in the body captures ITS iteration's cells rather than the
// last iteration's.
func TestBashPPLoopIterationsGetFreshCells(t *testing.T) {
	t.Parallel()

	t.Run("for, two identifiers and closures", func(t *testing.T) {
		t.Parallel()
		got := bashPPRun(t, `
for k in 1 2 3; do
	var i = 1
	var j = 10
	eval "closure$k() { echo \"k$k: i=\$i j=\$j\"; }"
	i=$((i + k))
	j=$((j + k))
done
closure1
closure2
closure3
echo "after: i=${i-unset} j=${j-unset}"
`)
		wantOutput(t, got, `k1: i=2 j=11
k2: i=3 j=12
k3: i=4 j=13
after: i=unset j=unset
`)
	})

	t.Run("while", func(t *testing.T) {
		t.Parallel()
		got := bashPPRun(t, `
n=0
while [ "$n" -lt 3 ]; do
	var w = 1
	n=$((n + 1))
	w=$((w + n))
	echo "w=$w"
done
echo "after=${w-unset}"
`)
		wantOutput(t, got, "w=2\nw=3\nw=4\nafter=unset\n")
	})

	t.Run("C-style for", func(t *testing.T) {
		t.Parallel()
		got := bashPPRun(t, `
for ((c = 0; c < 3; c++)); do
	var each = 1
	each=$((each + c))
	echo "each=$each"
done
echo "after=${each-unset}"
`)
		wantOutput(t, got, "each=1\neach=2\neach=3\nafter=unset\n")
	})

	t.Run("until", func(t *testing.T) {
		t.Parallel()
		got := bashPPRun(t, `
u=0
until [ "$u" -ge 2 ]; do
	var t = 1
	u=$((u + 1))
	echo "t=$t"
done
echo "after=${t-unset}"
`)
		wantOutput(t, got, "t=1\nt=1\nafter=unset\n")
	})
}

// Branch bodies and case clauses are blocks too. `else` reaches the same push
// as `then` because the parser models it as an IfClause with an empty
// condition, and a `;&` fallthrough gets a block of its own rather than
// continuing the clause it fell out of.
func TestBashPPBranchesAndClausesAreDistinctScopes(t *testing.T) {
	t.Parallel()

	t.Run("then and else", func(t *testing.T) {
		t.Parallel()
		got := bashPPRun(t, `
var x = 1
if true; then
	var x = 2
	echo "then=$x"
fi
if false; then
	var x = 3
else
	var x = 4
	echo "else=$x"
fi
echo "after=$x"
`)
		wantOutput(t, got, "then=2\nelse=4\nafter=1\n")
	})

	t.Run("elif", func(t *testing.T) {
		t.Parallel()
		got := bashPPRun(t, `
var x = 1
if false; then
	var x = 2
elif true; then
	var x = 3
	echo "elif=$x"
fi
echo "after=$x"
`)
		wantOutput(t, got, "elif=3\nafter=1\n")
	})

	t.Run("each case clause", func(t *testing.T) {
		t.Parallel()
		got := bashPPRun(t, `
var x = 1
for subject in a b; do
	case $subject in
	a)
		var x = 2
		echo "a=$x"
		;;
	b)
		var x = 3
		echo "b=$x"
		;;
	esac
done
echo "after=$x"
`)
		wantOutput(t, got, "a=2\nb=3\nafter=1\n")
	})

	t.Run("fallthrough clause", func(t *testing.T) {
		t.Parallel()
		got := bashPPRun(t, `
case a in
a)
	var x = 1
	echo "first=$x"
	;&
*)
	var x = 2
	echo "second=$x"
	;;
esac
echo "after=${x-unset}"
`)
		wantOutput(t, got, "first=1\nsecond=2\nafter=unset\n")
	})

	t.Run("brace group", func(t *testing.T) {
		t.Parallel()
		got := bashPPRun(t, `
var x = 1
{
	var x = 2
	echo "inner=$x"
}
echo "after=$x"
`)
		wantOutput(t, got, "inner=2\nafter=1\n")
	})
}

// A block may not declare the same identifier twice, exactly as in Go. This is
// the assertion the fresh-cell tests above lean on: without it, "the second
// iteration could redeclare i" would prove nothing, because redeclaration
// would always be allowed.
func TestBashPPRedeclarationInOneBlockIsRefused(t *testing.T) {
	t.Parallel()
	got := bashPPRun(t, "var x = 1\nvar x = 2\necho \"x=$x\"\n")
	if !strings.Contains(got, "x redeclared in this block") {
		t.Fatalf("a redeclaration was accepted:\n%s", got)
	}
	if !strings.Contains(got, "x=1\n") {
		t.Fatalf("a refused redeclaration still changed the binding:\n%s", got)
	}
}

// LangBash must be untouched. `var x = 1` is an ordinary three-argument
// command there, and the scope machinery must not exist at all — the hooks in
// vars.go are guarded on a nil scope precisely so that no other dialect pays
// for this, and a runner that built one would be evidence the guard moved.
func TestBashPPScopesAreOffOutsideTheDialect(t *testing.T) {
	t.Parallel()
	var out strings.Builder
	r, err := New(StdIO(nil, &out, &out), ExecHandlers(func(next ExecHandlerFunc) ExecHandlerFunc {
		return func(ctx context.Context, args []string) error {
			out.WriteString("argv=" + strings.Join(args, ",") + "\n")
			return nil
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	f, err := syntax.NewParser().Parse(strings.NewReader("var x = 1\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	wantOutput(t, out.String(), "argv=var,x,=,1\n")
	if r.bashPPScope != nil {
		t.Fatal("a LangBash runner built a bash++ scope; the nil guard is what keeps this free")
	}
}

// Concurrency, once more, but with the runner API rather than the shell's:
// several runners sharing nothing but the test's goroutines. This is the shape
// an embedder uses, and it is the one where a package-level cache or a scope
// hung off shared state would show up.
func TestBashPPConcurrentRunnersAreIndependent(t *testing.T) {
	t.Parallel()
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var out strings.Builder
			r, err := New(Lang(syntax.LangBashPP), StdIO(nil, &out, &out))
			if err != nil {
				t.Error(err)
				return
			}
			f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(
				strings.NewReader("var x = 1\n( x=2 ) &\nwait\nfor i in 1 2; do var y = 1; done\necho \"x=$x\"\n"), "")
			if err != nil {
				t.Error(err)
				return
			}
			if err := r.Run(context.Background(), f); err != nil {
				t.Error(err)
				return
			}
			if out.String() != "x=1\n" {
				t.Errorf("output = %q, want %q", out.String(), "x=1\n")
			}
		}()
	}
	wg.Wait()
}
