// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp_test

import (
	"context"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/go-quicktest/qt"
)

// The Sprint 114 P3-D runtime surface: `panic` and `recover`, and the
// panic-safe unwinding of the frames they abandon.
//
// Every case runs a whole script, because what these tests are about is what
// a script observes: which statements did NOT run, which cleanups did, which
// value a recover took, and what the shell's status was afterwards. A unit
// test on the panic state could pass while the statement after the panic site
// still ran, which is the one thing that must never happen.

// runBashPPPanic runs src and returns everything a script can observe: its
// combined output and the status Run reported.
func runBashPPPanic(t *testing.T, src string, opts ...interp.RunnerOption) (string, error) {
	t.Helper()
	var out strings.Builder
	all := append([]interp.RunnerOption{interp.Lang(syntax.LangBashPP)}, opts...)
	r := bashPPRunner(t, &out, all...)
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "")
	qt.Assert(t, qt.IsNil(err))
	runErr := r.Run(context.Background(), f)
	return out.String(), runErr
}

func TestBashPPPanicUnwinding(t *testing.T) {
	t.Parallel()

	tests := []struct{ name, src, want string }{
		{
			// The `echo after` is the assertion: a panic is a control
			// transfer, so execution does not resume at the panic site, in
			// the caller, or anywhere else.
			"an unrecovered panic abandons every frame and the script",
			"func f() {\n echo before\n panic(boom)\n echo after\n}\nf()\necho unreachable\n",
			"before\npanic: boom\n",
		}, {
			"a panic outside any function terminates at once",
			"echo one\npanic(top)\necho two\n",
			"one\npanic: top\n",
		}, {
			"a panic runs each abandoned frame's defers, most recent first",
			"func g(v) {\n echo \"g:$v\"\n}\nfunc f() {\n defer g(1)\n defer g(2)\n panic(p)\n}\nf()\n",
			"g:2\ng:1\npanic: p\n",
		}, {
			// Two frames, each with cleanups: the inner frame's run first
			// because it is abandoned first.
			"a panic unwinds nested frames in order",
			"func inner() {\n defer echo(innercleanup)\n panic(deep)\n}\nfunc outer() {\n defer echo(outercleanup)\n inner()\n echo unreachable\n}\nouter()\n",
			"innercleanup\noutercleanup\npanic: deep\n",
		}, {
			"a deferred call recovers the panic and the caller resumes",
			"func f() {\n defer func() {\n  v := recover()\n  echo \"got=$v\"\n }()\n panic(boom)\n echo unreachable\n}\nf()\necho resumed\n",
			"got=boom\nresumed\n",
		}, {
			// The recovering frame returns normally, so the frames between it
			// and the panic stay abandoned but the ones above it do not.
			"recovery stops the unwind at the recovering frame",
			"func inner() {\n panic(a)\n}\nfunc outer() {\n defer func() {\n  v := recover()\n  echo \"outer=$v\"\n }()\n inner()\n echo unreachable\n}\nouter()\necho end\n",
			"outer=a\nend\n",
		}, {
			"a panic breaks out of a loop rather than skipping iterations",
			"func f() {\n for i in 1 2 3; do\n  echo $i\n  if [ $i = 2 ]; then panic(stop); fi\n done\n echo unreachable\n}\nf()\n",
			"1\n2\npanic: stop\n",
		}, {
			// A shell function frame is abandoned by a panic exactly as a
			// Go-form one is: it does not get to consume the unwind the way it
			// consumes its own `return`.
			"a panic unwinds through a shell function frame",
			"sf() { echo insf; panic(viasf); echo unreachable; }\nfunc f() {\n defer func() {\n  v := recover()\n  echo \"got=$v\"\n }()\n sf\n echo unreachable\n}\nf()\necho end\n",
			"insf\ngot=viasf\nend\n",
		}, {
			"a panicking result call does not report an assignment mismatch",
			"func inner() int {\n panic(value)\n return 1\n}\nfunc outer() {\n defer func() {\n  v := recover()\n  echo \"got=$v\"\n }()\n x := inner()\n echo unreachable\n}\nouter()\necho end\n",
			"got=value\nend\n",
		}, {
			"a method's panic unwinds to its caller's recovering defer",
			"type T int\nfunc (v T) Blow() {\n panic(method)\n}\nfunc run() {\n defer func() {\n  v := recover()\n  echo \"caught=$v\"\n }()\n var t T = 3\n t.Blow()\n echo unreachable\n}\nrun()\n",
			"caught=method\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, _ := runBashPPPanic(t, tc.src)
			qt.Assert(t, qt.Equals(got, tc.want))
		})
	}
}

func TestBashPPRecoverIsDirectOnly(t *testing.T) {
	t.Parallel()

	tests := []struct{ name, src, want string }{
		{
			// Go: "the return value of recover is nil when the goroutine is
			// not panicking". The status distinguishes it from a recovered
			// empty payload.
			"recover outside a panic yields nothing",
			"func f() {\n defer func() {\n  v := recover()\n  echo \"none=[$v] status=$?\"\n }()\n echo body\n}\nf()\n",
			"body\nnone=[] status=1\n",
		}, {
			"recover in the panicking body itself does not recover",
			"func f() {\n v := recover()\n echo \"body=[$v] status=$?\"\n}\nf()\necho after\n",
			"body=[] status=1\nafter\n",
		}, {
			// One frame too deep: the deferred function called deep(), so
			// deep's recover was not called directly by a deferred function.
			"recover below the deferred call does not recover",
			"func deep() {\n v := recover()\n echo \"deep=[$v] status=$?\"\n}\nfunc f() {\n defer func() {\n  deep()\n }()\n panic(x)\n}\nf()\necho unreachable\n",
			"deep=[] status=1\npanic: x\n",
		}, {
			// `defer recover()` is the shape Go documents as not working:
			// recover IS the deferred call, so nothing deferred it in turn.
			"defer recover() does not recover",
			"func f() {\n defer recover()\n panic(boom)\n}\nf()\necho unreachable\n",
			"panic: boom\n",
		}, {
			"a bare recover() statement recovers without binding the payload",
			"func f() {\n defer func() {\n  recover()\n  echo recovered\n }()\n panic(x)\n}\nf()\necho end\n",
			"recovered\nend\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, _ := runBashPPPanic(t, tc.src)
			qt.Assert(t, qt.Equals(got, tc.want))
		})
	}
}

func TestBashPPNestedPanic(t *testing.T) {
	t.Parallel()

	tests := []struct{ name, src, want string }{
		{
			// The second panic replaces the first as the active one, and the
			// report names both so the original cause is not lost.
			"a panic in a cleanup replaces the active panic",
			"func f() {\n defer func() {\n  panic(second)\n }()\n panic(first)\n}\nf()\n",
			"panic: first\n\tpanic: second\n",
		}, {
			"a recovering cleanup may panic again",
			"func f() {\n defer func() {\n  v := recover()\n  echo \"caught=$v\"\n  panic(again)\n }()\n panic(first)\n}\nf()\n",
			"caught=first\npanic: again\n",
		}, {
			// Recovering the newest panic lets the older panic continue. Go's
			// runtime pins this recursive-panic rule explicitly.
			"recovering a nested panic resumes the older unwind",
			"func f() {\n defer func() {\n  defer func() {\n   v := recover()\n   echo \"inner=$v\"\n  }()\n  panic(second)\n }()\n panic(first)\n}\nf()\necho end\n",
			"inner=second\npanic: first\n",
		}, {
			"a later outer cleanup may recover the older panic",
			"func f() {\n defer func() {\n  v := recover()\n  echo \"outer=$v\"\n }()\n defer func() {\n  defer func() {\n   v := recover()\n   echo \"inner=$v\"\n  }()\n  panic(second)\n }()\n panic(first)\n}\nf()\necho end\n",
			"inner=second\nouter=first\nend\n",
		}, {
			"a cleanup's own panic still lets the remaining cleanups run",
			"func f() {\n defer echo(last)\n defer func() {\n  panic(second)\n }()\n panic(first)\n}\nf()\n",
			"last\npanic: first\n\tpanic: second\n",
		}, {
			// `defer panic(v)` fixes its argument where the defer ran, like
			// every other deferred call's arguments.
			"a deferred panic takes the value held at defer time",
			"func f() {\n v=1\n defer panic($v)\n v=2\n}\nf()\n",
			"panic: 1\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, _ := runBashPPPanic(t, tc.src)
			qt.Assert(t, qt.Equals(got, tc.want))
		})
	}
}

// TestBashPPPanicNamedResults pins Go's ordering rule: results are set before
// the deferred calls run, so a deferred call can change a named one — which is
// the only way a recovered frame can produce a value at all.
func TestBashPPPanicNamedResults(t *testing.T) {
	t.Parallel()

	tests := []struct{ name, src, want string }{
		{
			"a recovering defer supplies the named result",
			"func f() (n int) {\n defer func() {\n  recover()\n  n=99\n }()\n n=1\n panic(boom)\n}\nx := f()\necho \"x=$x\"\n",
			"x=99\n",
		}, {
			"an ordinary defer amends the returned named result",
			"func f() (n int) {\n defer func() {\n  n=$((n + 1))\n }()\n return 5\n}\nx := f()\necho \"x=$x\"\n",
			"x=6\n",
		}, {
			// An unnamed result has no binding a deferred call could reach, in
			// Go or here, so a recovered frame yields its zero value.
			"a recovered frame with an unnamed result yields the zero value",
			"func f() int {\n defer func() {\n  recover()\n }()\n panic(x)\n return 5\n}\ny := f()\necho \"y=[$y] status=$?\"\n",
			"y=[] status=0\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, _ := runBashPPPanic(t, tc.src)
			qt.Assert(t, qt.Equals(got, tc.want))
		})
	}
}

// TestBashPPPanicTerminationPolicy pins what an unrecovered panic does to the
// shell, and what outranks it.
func TestBashPPPanicTerminationPolicy(t *testing.T) {
	t.Parallel()

	t.Run("unrecovered panic exits 2", func(t *testing.T) {
		t.Parallel()
		out, err := runBashPPPanic(t, "func f() {\n panic(boom)\n}\nf()\necho unreachable\n")
		qt.Assert(t, qt.Equals(out, "panic: boom\n"))
		qt.Assert(t, qt.ErrorIs(err, interp.ExitStatus(2)))
	})
	t.Run("recovered panic exits cleanly", func(t *testing.T) {
		t.Parallel()
		out, err := runBashPPPanic(t, "func f() {\n defer func() {\n  recover()\n }()\n panic(boom)\n}\nf()\necho \"status=$?\"\n")
		qt.Assert(t, qt.Equals(out, "status=0\n"))
		qt.Assert(t, qt.IsNil(err))
	})
	t.Run("an explicit exit in a cleanup outranks the panic", func(t *testing.T) {
		t.Parallel()
		// `os.Exit` in a deferred function terminates a Go program with its
		// own status and prints no panic message; so does `exit` here.
		out, err := runBashPPPanic(t, "func f() {\n defer func() {\n  echo cleanup\n  exit 3\n }()\n panic(boom)\n}\nf()\necho unreachable\n")
		qt.Assert(t, qt.Equals(out, "cleanup\n"))
		qt.Assert(t, qt.ErrorIs(err, interp.ExitStatus(3)))
	})
	t.Run("a hard exit skips the remaining cleanups", func(t *testing.T) {
		t.Parallel()
		out, err := runBashPPPanic(t, "func f() {\n defer echo(never)\n defer func() {\n  exit 4\n }()\n panic(boom)\n}\nf()\n")
		qt.Assert(t, qt.Equals(out, ""))
		qt.Assert(t, qt.ErrorIs(err, interp.ExitStatus(4)))
	})
	t.Run("a panic does not cross a subshell boundary", func(t *testing.T) {
		t.Parallel()
		// A subshell is a process boundary: the panic settles inside it, the
		// subshell exits 2 like any unrecovered panic, and the parent shell
		// observes that status and carries on.
		out, err := runBashPPPanic(t,
			"func p() {\n panic(inside)\n}\nwrap() { p(); }\necho before\n( wrap )\necho \"sub=$?\"\necho after\n")
		qt.Assert(t, qt.Equals(out, "before\npanic: inside\nsub=2\nafter\n"))
		qt.Assert(t, qt.IsNil(err))
	})
}

// TestBashPPPanicDiagnostics covers the shapes Go rejects at compile time.
// This shell has no compile step, so each is a runtime diagnostic instead.
func TestBashPPPanicDiagnostics(t *testing.T) {
	t.Parallel()

	tests := []struct{ name, src, want string }{
		{
			"panic takes exactly one argument",
			"func f() {\n panic(a, b)\n}\nf()\n",
			"panic: takes exactly one argument\n",
		}, {
			"recover takes none",
			"func f() {\n defer func() {\n  v := recover(1)\n }()\n panic(x)\n}\nf()\n",
			"recover: takes no arguments\npanic: x\n",
		}, {
			"recover yields exactly one value",
			"func f() {\n defer func() {\n  a, b := recover()\n }()\n panic(x)\n}\nf()\n",
			"assignment mismatch: 2 variable(s) but 1 value(s)\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, _ := runBashPPPanic(t, tc.src)
			qt.Assert(t, qt.Equals(got, tc.want))
		})
	}
}

// TestBashPPPanicIsPredeclaredNotReserved proves the names stay Go's
// predeclared identifiers rather than becoming keywords: a session that
// declares its own wins, and a shell script that defines a function of that
// name is untouched.
func TestBashPPPanicIsPredeclaredNotReserved(t *testing.T) {
	t.Parallel()

	t.Run("a declared func shadows the predeclared one", func(t *testing.T) {
		t.Parallel()
		out, err := runBashPPPanic(t, "func panic(msg string) {\n echo \"shadow:$msg\"\n}\npanic(hello)\necho after\n")
		qt.Assert(t, qt.Equals(out, "shadow:hello\nafter\n"))
		qt.Assert(t, qt.IsNil(err))
	})
	t.Run("a shell function definition keeps its meaning", func(t *testing.T) {
		t.Parallel()
		out, err := runBashPPPanic(t, "recover() { echo shellfunc; }\nrecover\necho after\n")
		qt.Assert(t, qt.Equals(out, "shellfunc\nafter\n"))
		qt.Assert(t, qt.IsNil(err))
	})
}

// TestBashPPPanicDialectIsolation proves panic is dialect state like every
// other Bash++ extension: Classic Bash and POSIX mode never panic.
func TestBashPPPanicDialectIsolation(t *testing.T) {
	t.Parallel()

	const want = "bash++: panic(...) is recognized but not implemented in this phase\nafter\n"
	t.Run("classic bash", func(t *testing.T) {
		t.Parallel()
		var out strings.Builder
		r := bashPPRunner(t, &out)
		f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader("panic(boom)\necho after\n"), "")
		qt.Assert(t, qt.IsNil(err))
		_ = r.Run(context.Background(), f)
		qt.Assert(t, qt.Equals(out.String(), want))
	})
	t.Run("posix mode", func(t *testing.T) {
		t.Parallel()
		got, _ := runBashPPPanic(t, "set -o posix\npanic(boom)\necho after\n")
		qt.Assert(t, qt.Equals(got, want))
	})
}
