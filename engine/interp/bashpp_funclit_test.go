// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp_test

import (
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/go-quicktest/qt"
)

// The Sprint 114 P3-B runtime surface: function literals — bound, invoked,
// deferred and returned — and variadic parameters with their spread.
//
// Every case runs a script end to end rather than poking at the runner, so
// what is pinned is what a script observes: which value a closure saw, how
// many arguments a variadic parameter collected, and which diagnostic a
// mismatch produced.

func runBashPPLit(t *testing.T, src string) string {
	t.Helper()
	var out strings.Builder
	r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP))
	bashPPRun(t, r, src)
	return out.String()
}

func TestBashPPFuncLitRuntime(t *testing.T) {
	t.Parallel()

	tests := []struct{ name, src, want string }{
		{
			"bound literal is called by its name",
			"greet := func(who string) {\n echo \"hi $who\"\n}\ngreet(bob)\n",
			"hi bob\n",
		}, {
			// The zero-argument call is only recognizable because the binding
			// registered the name; see Parser.bashppRegisterFunc.
			"bound literal takes no arguments",
			"hi := func() {\n echo hi\n}\nhi()\n",
			"hi\n",
		}, {
			"immediate invocation",
			"func(n int) {\n echo \"n=$n\"\n}(3)\n",
			"n=3\n",
		}, {
			"immediate invocation into a binding",
			"n := func() int {\n return 7\n}()\necho \"n=$n\"\n",
			"n=7\n",
		}, {
			"deferred closure runs as the frame unwinds",
			"func f() {\n defer func() {\n  echo done\n }()\n echo body\n}\nf()\n",
			"body\ndone\n",
		}, {
			// Go evaluates a deferred call's arguments where the defer runs,
			// not where it fires; the closure sees the value v had then.
			"deferred closure captures its arguments at defer time",
			"func f() {\n v=1\n defer func(seen int) {\n  echo \"seen=$seen\"\n }($v)\n v=2\n}\nf()\n",
			"seen=1\n",
		}, {
			"closure observes a later mutation of a captured cell",
			"var c = 1\nbump := func() {\n echo \"c=$c\"\n}\nc=9\nbump()\n",
			"c=9\n",
		}, {
			// Each invocation of the factory has its own scope, so the two
			// closures close over different cells rather than the last one.
			"capture is per invocation",
			"func mk(n int) func {\n return func(add int) int {\n  return $((n + add))\n }\n}\na := mk(1)\nb := mk(2)\nx := a(10)\ny := b(10)\necho \"$x $y\"\n",
			"11 12\n",
		}, {
			// The cell outlives the frame that declared it because the capture
			// holds it: this is the escape case, and the mutation proves the
			// closure kept the cell rather than a copy of its value.
			"closure escapes its factory and mutates the captured cell",
			"func counter() func {\n var n = 0\n return func() int {\n  n=$((n + 1))\n  return n\n }\n}\nnext := counter()\np := next()\nq := next()\necho \"$p $q\"\n",
			"1 2\n",
		}, {
			"closure passed as a func-typed parameter",
			"func apply(cb func, n int) {\n cb($n)\n}\nshow := func(v int) {\n echo \"v=$v\"\n}\napply($show, 4)\n",
			"v=4\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			qt.Assert(t, qt.Equals(runBashPPLit(t, tc.src), tc.want))
		})
	}
}

func TestBashPPVariadicRuntime(t *testing.T) {
	t.Parallel()

	// A variadic parameter binds as an indexed variable, so the body reads it
	// with the array spellings the shell already has.
	const sum = "func sum(nums ...int) int {\n t=0\n for v in \"${nums[@]}\"; do t=$((t + v)); done\n return $t\n}\n"

	tests := []struct{ name, src, want string }{
		{"no arguments", sum + "x := sum()\necho \"x=$x\"\n", "x=0\n"},
		{"one argument", sum + "x := sum(4)\necho \"x=$x\"\n", "x=4\n"},
		{"many arguments", sum + "x := sum(1, 2, 3)\necho \"x=$x\"\n", "x=6\n"},
		{
			"fixed parameters keep their own values",
			"func tag(p string, rest ...int) {\n echo \"$p:${#rest[@]}:${rest[0]}\"\n}\ntag(hi, 7, 8)\n",
			"hi:2:7\n",
		}, {
			"an unnamed variadic group accepts and discards",
			"func any(...int) {\n echo ok\n}\nany(1, 2)\n",
			"ok\n",
		}, {
			// The spread is what makes a variadic parameter forwardable, which
			// is the only way to write a wrapper without knowing the arity.
			"spread forwards a variadic parameter",
			sum + "func fwd(rest ...int) int {\n t := sum(rest...)\n return t\n}\nx := fwd(4, 5)\necho \"x=$x\"\n",
			"x=9\n",
		}, {
			"spread of an empty slice passes nothing",
			sum + "func fwd(rest ...int) int {\n t := sum(rest...)\n return t\n}\nx := fwd()\necho \"x=$x\"\n",
			"x=0\n",
		}, {
			"spread of a shell array",
			sum + "xs=(1 2 3)\nx := sum(xs...)\necho \"x=$x\"\n",
			"x=6\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			qt.Assert(t, qt.Equals(runBashPPLit(t, tc.src), tc.want))
		})
	}
}

// TestBashPPFuncLitDiagnostics covers the mismatches. Each shape is Class R —
// bash rejects it outright — so a diagnostic takes nothing away from a working
// script, and each one names the offending function and value rather than
// failing silently with a zero value.
func TestBashPPFuncLitDiagnostics(t *testing.T) {
	t.Parallel()

	tests := []struct{ name, src, want string }{
		{
			"too many arguments",
			"greet := func(a int) {\n echo $a\n}\ngreet(1, 2)\n",
			"greet: expected 1 argument(s), got 2\n",
		}, {
			"too few arguments",
			"greet := func(a, b int) {\n echo $a\n}\ngreet(1)\n",
			"greet: expected 2 argument(s), got 1\n",
		}, {
			"variadic minimum not met",
			"func tag(p string, rest ...int) {\n echo $p\n}\ntag()\n",
			"tag: expected at least 1 argument(s), got 0\n",
		}, {
			"argument is not a value of the declared type",
			"greet := func(a int) {\n echo $a\n}\ngreet(abc)\n",
			"greet: cannot use \"abc\" as int value for parameter a\n",
		}, {
			"a variadic element is checked too",
			"func sum(nums ...int) {\n echo hi\n}\nsum(1, two)\n",
			"sum: cannot use \"two\" as int value for parameter nums\n",
		}, {
			"a func-typed parameter needs a function",
			"func apply(cb func) {\n echo hi\n}\napply(nope)\n",
			"apply: cannot use \"nope\" as func value for parameter cb\n",
		}, {
			"spreading into a non-variadic function",
			"func one(a int) {\n echo $a\n}\nxs=(1 2)\none(xs...)\n",
			"cannot use ... in call to non-variadic one\n",
		}, {
			// An untyped parameter declares nothing, so it admits anything —
			// the check is narrow on purpose.
			"an untyped parameter admits any value",
			"show := func(v) {\n echo \"v=$v\"\n}\nshow(anything)\n",
			"v=anything\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			qt.Assert(t, qt.Equals(runBashPPLit(t, tc.src), tc.want))
		})
	}
}

// TestBashPPFuncLitStatusBridging proves the delivered P3-A contract still
// holds for literals: a call's exit status is the shell's, so `&&`, `if` and
// `$?` keep working across a closure.
func TestBashPPFuncLitStatusBridging(t *testing.T) {
	t.Parallel()

	tests := []struct{ name, src, want string }{
		{
			"body status reaches the caller",
			"boom := func(n int) {\n return $n\n}\nboom(3)\necho \"status=$?\"\n",
			"status=3\n",
		}, {
			"a returned value succeeds",
			"ok := func() int {\n return 5\n}\nx := ok()\necho \"x=$x status=$?\"\n",
			"x=5 status=0\n",
		}, {
			"a deferred closure's failure is observable",
			"func f() int {\n defer func() {\n  return 7\n }()\n return 2\n}\nx := f()\necho \"x=$x status=$?\"\n",
			"x=2 status=7\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			qt.Assert(t, qt.Equals(runBashPPLit(t, tc.src), tc.want))
		})
	}
}

// TestBashPPFuncLitSubshellIsolation proves a closure crosses the subshell
// boundary the way every other variable does: the child gets a private copy of
// the captured cells, so a mutation inside `( … )` is not visible outside it.
//
// This is not a detail. Subshells here are goroutines rather than fork(), so a
// shared cell would be both wrong and a data race the detector reports.
func TestBashPPFuncLitSubshellIsolation(t *testing.T) {
	t.Parallel()

	const src = "func counter() func {\n" +
		" var n = 0\n" +
		" return func() int {\n" +
		"  n=$((n + 1))\n" +
		"  return n\n" +
		" }\n" +
		"}\n" +
		"next := counter()\n" +
		"(\n" +
		" a := next()\n" +
		" b := next()\n" +
		" echo \"inside $a $b\"\n" +
		")\n" +
		"c := next()\n" +
		"echo \"outside $c\"\n"
	qt.Assert(t, qt.Equals(runBashPPLit(t, src), "inside 1 2\noutside 1\n"))
}

// TestBashPPFuncLitDialectGate proves the literal is gated exactly as every
// other Bash++ node is: the parser keeps the tree under POSIX mode so the
// runtime can refuse it, and a runner with the extensions off says so rather
// than binding a closure nobody could call.
func TestBashPPFuncLitDialectGate(t *testing.T) {
	t.Parallel()

	const src = "greet := func(who string) {\n echo hi\n}\ngreet(bob)\n"
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "")
	qt.Assert(t, qt.IsNil(err))

	for _, tc := range []struct {
		name string
		opts []interp.RunnerOption
	}{
		{"posix mode", []interp.RunnerOption{
			interp.Lang(syntax.LangBashPP), interp.WithPosixMode(true),
		}},
		{"classic bash runner", []interp.RunnerOption{interp.Lang(syntax.LangBash)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out strings.Builder
			r := bashPPRunner(t, &out, tc.opts...)
			_ = r.Run(t.Context(), f)
			qt.Assert(t, qt.IsTrue(strings.Contains(out.String(), "extensions disabled")),
				qt.Commentf("output %q", out.String()))
		})
	}
}
