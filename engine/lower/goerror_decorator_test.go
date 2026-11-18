//go:build full

package lower_test

import (
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/lower"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// B23 (Sprint 221, story d50392e49a40): `@go.error()` adapts the existing
// Bash# value/error convention — declared results plus the call's completed
// exit status — into Go's `(T, error)` convention, as an ordinary generated
// Go wrapper at lowering time. Every case is compiled, built, executed, and
// diffed byte for byte (stdout, stderr, exit status) against the interpreter
// running the same source, so each row is a measured interpreted/lowered
// parity claim.
//
// Source-derived fixtures, pinned provenance (full table in
// plan-b23-goerror-decorator.md):
//   - golang/go tag go1.25.0, src/builtin/builtin.go + Effective Go §Errors
//     (BSD-3-Clause): success is value + nil error; a zero value with a nil
//     error is a success, distinguishable from failure — the reason the
//     convention exists.
//   - golang/go tag go1.25.0, src/os/exec_posix.go ProcessState.String and
//     src/os/exec/exec.go ExitError (BSD-3-Clause): a nonzero exit status IS
//     the error, message spelling "exit status %d".
//   - this repo commit 47433440 (BSD-3-Clause, the bashy authors):
//     lower/decorator_lowering_test.go TestDecoratedCallableExecution rows
//     "trace sees name and status" and "skip denies the body...", and
//     lower/decorator_contract_test.go "panic_defer" — the decorator
//     ordering/error behavior the adapter must preserve.
func TestGoErrorDecorator(t *testing.T) {
	for _, tc := range []struct{ name, src, out, err string }{
		{
			// golang/go builtin error convention: success = (value, nil).
			"value_success",
			`@go.error()
func pick(a, b int) int { return $b }
v, err := pick(1, 7)
echo "v=$v err=[$err] status=$?"
`,
			"v=7 err=[] status=0\n", "",
		},
		{
			// Effective Go §Errors: the zero value with a nil error is a
			// SUCCESS — err, not the value, separates it from failure.
			"zero_value_success",
			`@go.error()
func zero() int { return 0 }
v, err := zero()
echo "v=$v err=[$err] status=$?"
`,
			"v=0 err=[] status=0\n", "",
		},
		{
			// os.ProcessState.String: a nonzero status is the error, spelled
			// "exit status N". Explicit `return value` resets the body status
			// under the existing callable lowering, so a rung marks the settled
			// call status after seeing the body result.
			"status_error",
			`func mark(c *Call) {
	c.Next()
	c.Status = 1
}
@mark()
@go.error()
func deploy(env string) string {
	echo "deploying $env"
	return "partial"
}
v, err := deploy(prod)
echo "v=$v err=[$err]"
`,
			"deploying prod\nv=partial err=[deploy: exit status 1]\n", "",
		},
		{
			// The adapter preserves the classic status contract: $? after the
			// call is the body's failing status, exactly as undecorated
			// (repo 47433440, "trace sees name and status").
			"status_error_preserves_status",
			`func mark3(c *Call) {
	c.Next()
	c.Status = 3
}
@mark3()
@go.error()
func fail3() int {
	return 9
}
v, err := fail3()
echo "v=$v err=[$err] status=$?"
`,
			"v=9 err=[fail3: exit status 3] status=3\n", "",
		},
		{
			// Repo 47433440 "skip denies the body": a rung that skips Next
			// yields zero results and the decorator's status; the conversion
			// runs after the chain settles, so the caller still gets the
			// (zero, error) pair.
			"short_circuit_converts",
			`func deny(c *Call) {
	echo "denied"
	c.Status = 3
}
@deny()
@go.error()
func secret() int { echo leaked; return 1 }
v, err := secret()
echo "v=<$v> err=[$err] status=$?"
`,
			"denied\nv=<0> err=[secret: exit status 3] status=3\n", "",
		},
		{
			// Rungs of the same chain observe the body's own results and
			// Status — the error result does not exist inside the chain
			// (adapter phase order: invoke, then convert output).
			"rungs_see_body_results",
			`func trace(c *Call) {
	c.Next()
	r := c.Results[0]
	st := c.Status
	echo "trace first=$r status=$st"
}
@trace()
@go.error()
func answer() int {
	return 42
}
v, err := answer()
echo "v=$v err=[$err] status=$?"
`,
			"trace first=42 status=0\nv=42 err=[] status=0\n", "",
		},
		{
			// Repo 47433440 "panic_defer": a panic is not an error value —
			// Bash# adds no exception handling — so the adapter must let it
			// unwind; a recovering outer decorator still sees it, and the
			// status it leaves is what converts.
			"panic_propagates",
			`func catch(c *Call) {
	defer func() {
		r := recover()
		echo "caught:[$r]"
		c.Status = 1
	}()
	c.Next()
}
@catch()
@go.error()
func panic_maker() int {
	panic("boom")
}
v, err := panic_maker()
echo "v=$v err=[$err] status:[$?]"
`,
			"caught:[boom]\nv=0 err=[panic_maker: exit status 1] status:[1]\n", "",
		},
		{
			// A zero-result function adapts to a bare error result — the
			// `func F() error` half of the Go convention.
			"error_only",
			`func mark(c *Call) {
	c.Next()
	c.Status = 1
}
@mark()
@go.error()
func checkFail() {
}
@go.error()
func checkOK() {
}
err := checkFail()
echo "err=[$err] status=$?"
err2 := checkOK()
echo "err2=[$err2] status=$?"
`,
			"err=[checkFail: exit status 1] status=1\nerr2=[] status=0\n", "",
		},
		{
			// The adapted surface is the function's one signature: a handle
			// keeps the decoration and the (T, error) shape.
			"function_handle",
			`func mark(c *Call) {
	c.Next()
	c.Status = 1
}
@mark()
@go.error()
func f(n int) int {
	return $((n * 2))
}
g := f
v, err := g(0)
echo "v=$v err=[$err] status=$?"
`,
			"v=0 err=[f: exit status 1] status=1\n", "",
		},
		{
			// Multiple declared values keep their positions; the error is
			// strictly trailing.
			"multi_value",
			`@go.error()
func pair(a, b int) (int, string) { return $((a + b)), "sum" }
x, label, err := pair(2, 3)
echo "x=$x label=$label err=[$err] status=$?"
`,
			"x=5 label=sum err=[] status=0\n", "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, stderr := execute(t, compile(t, tc.src))
			if out != tc.out || stderr != tc.err {
				t.Fatalf("stdout=%q stderr=%q, want stdout=%q stderr=%q", out, stderr, tc.out, tc.err)
			}
		})
	}
}

// TestGoErrorDecoratorNilComparison pins the one consumer shape the adapter
// promises beyond interpolation: comparing the trailing error against nil.
func TestGoErrorDecoratorNilComparison(t *testing.T) {
	const src = `@go.error()
func okValue() int {
	return 5
}
func mark(c *Call) {
	c.Next()
	c.Status = 1
}
@mark()
@go.error()
func failValue() int {
	return 0
}
v, err := failValue()
failed := err != nil
echo "failed=$failed"
v2, err2 := okValue()
ok := err2 == nil
echo "ok=$ok"
`
	out, stderr := execute(t, compile(t, src))
	want := "failed=true\nok=true\n"
	if out != want || stderr != "" {
		t.Fatalf("stdout=%q stderr=%q, want stdout=%q", out, stderr, want)
	}
}

// TestGoErrorDecoratorInfrastructureFailure pins that a decorator-machinery
// failure — an undefined native decorator, or a rung that rewrites Args or
// Results into a shape the target cannot accept — is a hard failure in BOTH
// engines: no result binds, `$?` is 2, and the caller reports the
// assignment mismatch, exactly as the interpreter returns nil,false. The
// adapter must NOT convert an infrastructure failure into an ordinary
// (zero, "exit status N") pair; the trailing error stays its zero value on the
// failure path and is minted only from a completed call's status.
func TestGoErrorDecoratorInfrastructureFailure(t *testing.T) {
	for _, tc := range []struct{ name, src, out, err string }{
		{
			// An undefined native decorator fails the chain (Decorate == false).
			"undefined_native",
			`@nosuch()
@go.error()
func f() int { return 1 }
v, err := f()
echo "after v=$v err=[$err] status=$?"
`,
			"after v= err=[] status=2\n",
			"BASHPP-EDECO-UNDEF: decorator nosuch is not defined\nassignment mismatch: 2 variable(s) but 0 value(s)\n",
		},
		{
			// A rung rewrites Args to a value the target's parameter cannot
			// accept; the body binder rejects it and the chain fails.
			"invalid_args_rewrite",
			`func bad(c *Call) {
	c.Args = []any{"x"}
	c.Next()
}
@bad()
@go.error()
func f(n int) int { return $n }
v, err := f(5)
echo "after v=$v err=[$err] status=$?"
`,
			"after v= err=[] status=2\n",
			"BASHPP-EDECO-ARG: f: cannot use \"x\" as int value for parameter n\nassignment mismatch: 2 variable(s) but 0 value(s)\n",
		},
		{
			// A rung rewrites Results to the wrong count (DecoratedResults ==
			// false).
			"invalid_results_count",
			`func bad(c *Call) {
	c.Next()
	c.Results = []any{1, 2}
}
@bad()
@go.error()
func f() int { return 1 }
v, err := f()
echo "after v=$v err=[$err] status=$?"
`,
			"after v= err=[] status=2\n",
			"BASHPP-EDECO-RESULT: f declares 1 result(s); decorator supplied 2\nassignment mismatch: 2 variable(s) but 0 value(s)\n",
		},
		{
			// A rung rewrites Results to the wrong type (DecoratedResult ==
			// false).
			"invalid_results_type",
			`func bad(c *Call) {
	c.Next()
	c.Results = []any{"notint"}
}
@bad()
@go.error()
func f() int { return 1 }
v, err := f()
echo "after v=$v err=[$err] status=$?"
`,
			"after v= err=[] status=2\n",
			"BASHPP-EDECO-RESULT: f: cannot use \"notint\" as int result 1\nassignment mismatch: 2 variable(s) but 0 value(s)\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, stderr := execute(t, compile(t, tc.src))
			if out != tc.out || stderr != tc.err {
				t.Fatalf("stdout=%q stderr=%q, want stdout=%q stderr=%q", out, stderr, tc.out, tc.err)
			}
		})
	}
}

// TestGoErrorDecoratorFailureReturnsZeroError guards the lowered shape behind
// the parity above: on every infrastructure-failure branch the entry returns
// the declared zero values — a nil trailing error — rather than minting
// rt.GoError from the failure status. GoError is emitted only on the success
// paths (the settled call's status).
func TestGoErrorDecoratorFailureReturnsZeroError(t *testing.T) {
	src := `func mark(c *Call) { c.Next(); c.Status = 1 }
@mark()
@go.error()
func f() int { return 7 }
v, err := f()
echo "v=$v err=[$err]"
`
	source := string(compile(t, src).Source)
	// The single body result's zero pairs with a zero error on the failure
	// branches; the buggy shape returned that zero result beside a minted
	// GoError. The success return instead pairs the settled `final` value with
	// GoError, so this signature is specific to the failure branches.
	if strings.Contains(source, "_result0, __bpp0_rt.GoError(") {
		t.Fatalf("infrastructure-failure branch mints a spurious GoError instead of a zero error:\n%s", source)
	}
	// GoError must still be minted on the success paths.
	if !strings.Contains(source, "__bpp0_rt.GoError(") {
		t.Fatalf("success path no longer mints GoError:\n%s", source)
	}
}

// TestGoErrorDecoratorStatusBoundary pins the 8-bit status wrap end to end: a
// decorator-set c.Status of 0, the maximum 255, and out-of-range 256/257
// produce the same `$?` and trailing-error presence in the compiled program
// and the interpreter. A status that wraps to zero is a success (nil error).
func TestGoErrorDecoratorStatusBoundary(t *testing.T) {
	for _, tc := range []struct{ name, status, out string }{
		{"zero_is_success", "0", "v=7 err=[] status=0\n"},
		{"max_8bit_is_failure", "255", "v=7 err=[f: exit status 255] status=255\n"},
		{"out_of_range_wraps_to_zero", "256", "v=7 err=[] status=0\n"},
		{"out_of_range_wraps_into_range", "257", "v=7 err=[f: exit status 1] status=1\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := "func mark(c *Call) { c.Next(); c.Status = " + tc.status + " }\n" +
				"@mark()\n@go.error()\nfunc f() int { return 7 }\n" +
				"v, err := f()\necho \"v=$v err=[$err] status=$?\"\n"
			out, stderr := execute(t, compile(t, src))
			if out != tc.out || stderr != "" {
				t.Fatalf("stdout=%q stderr=%q, want stdout=%q", out, stderr, tc.out)
			}
		})
	}
}

// TestGoErrorDecoratorRefusals pins the lowering-time diagnostics: the marker
// is bounded to exactly the shape the story admits, and the rest of the
// namespaced space stays reserved.
func TestGoErrorDecoratorRefusals(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{
			"arguments refused",
			"@go.error(1)\nfunc f() int { return 1 }\nf()\n",
			"BASHPP-EDECO-GOERROR",
		},
		{
			"twice refused",
			"@go.error()\n@go.error()\nfunc f() int { return 1 }\nf()\n",
			"BASHPP-EDECO-GOERROR",
		},
		{
			"trailing error refused",
			"@go.error()\nfunc f() (int, error) { return 1, nil }\nf()\n",
			"BASHPP-EDECO-GOERROR",
		},
		{
			"method refused",
			"type T int\n@go.error()\nfunc (v T) M() int { return 1 }\n",
			"BASHPP-EDECO-GOERROR",
		},
		{
			"generic refused",
			"@go.error()\nfunc f[T any](v T) T { return v }\nf(1)\n",
			"BASHPP-EDECO-GOERROR",
		},
		{
			"shell function refused",
			"@go.error()\nf() { echo hi; }\nf\n",
			"", // decorated shell functions are refused by lowering already
		},
		{
			"other namespaced names stay reserved",
			"@go.retry()\nfunc f() int { return 1 }\nf()\n",
			"BASHPP-EDECO-RESERVED",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(tc.src), "input.bpp")
			if err != nil {
				t.Fatal(err)
			}
			_, err = lower.Compile(f, lower.Options{})
			if err == nil {
				t.Fatalf("compile succeeded, want a diagnostic")
			}
			if tc.want != "" && !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("diagnostic %q does not name %s", err, tc.want)
			}
		})
	}
}
