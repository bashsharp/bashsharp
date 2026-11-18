//go:build full

package lower_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/lower"
)

// Sources are copied unchanged from interp/bashpp_generic_method_test.go:
// TestBashPPIndependentMethodTypeParamsRuntime. Each artifact is built by the
// actual compiler and execute removes its Go source before native execution.
func TestGenericMethodAcceptance(t *testing.T) {
	cases := []struct{ name, source, want string }{
		{"explicit instantiation on an ordinary receiver", "type R int\nfunc (r R) M[T any](v T) T {\n return v\n}\nvar r R = 1\nx := r.M[int](7)\necho \"x=$x\"\n", "x=7\n"},
		{"inference on an ordinary receiver", "type R int\nfunc (r R) M[T any](v T) T {\n return v\n}\nvar r R = 1\nx := r.M(hi)\necho \"x=$x\"\n", "x=hi\n"},
		{"constrained method type parameter", "type Num interface { ~int }\ntype R int\nfunc (r R) Sum[T Num](a T, b T) T {\n return $((a+b))\n}\nvar r R = 1\nx := r.Sum[int](3, 4)\necho \"x=$x\"\n", "x=7\n"},
		{"constrained method type parameter inferred", "type Num interface { ~int }\ntype R int\nfunc (r R) Sum[T Num](a T, b T) T {\n return $((a+b))\n}\nvar r R = 1\nx := r.Sum(3, 4)\necho \"x=$x\"\n", "x=7\n"},
		{"two method type parameters", "type R int\nfunc (r R) Pick[T any, U any](a T, b U) T {\n return a\n}\nvar r R = 1\nx := r.Pick[int,string](3, hi)\necho \"x=$x\"\n", "x=3\n"},
		{"generic receiver and method scopes are independent", "type Box[T any] struct { Value T }\nfunc (b Box[T]) Pair[U any](u U) U {\n return u\n}\nvar b Box[int] = Box[int]{Value: 1}\nx := b.Pair[string](hey)\necho \"x=$x\"\n", "x=hey\n"},
		{"generic receiver binding survives method instantiation", "type Box[T any] struct { Value T }\nfunc (b Box[T]) Take[U any](t T, u U) T {\n return t\n}\nvar b Box[int] = Box[int]{Value: 1}\nx := b.Take[string](4, hey)\necho \"x=$x\"\n", "x=4\n"},
		{"pointer receiver", "type R int\nfunc (p *R) M[T any](v T) T {\n return v\n}\nvar r R = 1\nx := r.M[int](5)\necho \"x=$x\"\n", "x=5\n"},
		{"method expression", "type R int\nfunc (r R) M[T any](v T) T {\n return v\n}\nvar r R = 1\nx := R.M[int](r, 5)\necho \"x=$x\"\n", "x=5\n"},
		{"non-generic method is unaffected", "type R int\nfunc (r R) M(v int) int {\n return v\n}\nvar r R = 1\nx := r.M(5)\necho \"x=$x\"\n", "x=5\n"},
		{"generic receiver method without its own parameters is unaffected", "type Box[T any] struct { Value T }\nfunc (b Box[T]) Get(v T) T {\n return v\n}\nvar b Box[int] = Box[int]{Value: 1}\nx := b.Get(9)\necho \"x=$x\"\n", "x=9\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, diagnostic, status := genericMethodOracle(t, tc.source)
			t.Logf("interpreter stdout=%q stderr=%q status=%d", out, diagnostic, status)
			if out != tc.want || diagnostic != "" || status != 0 {
				t.Fatalf("public interpreter contract changed")
			}
			execute(t, compile(t, tc.source))
		})
	}
}

// These unchanged public negative sources must retain complete diagnostics,
// rather than a generic Go error or an unsupported classification.
func TestGenericMethodRejectionAcceptance(t *testing.T) {
	cases := []struct{ name, source, want string }{
		{"type arguments on a non-generic method", "type R int\nfunc (r R) M(v int) int {\n return v\n}\nvar r R = 1\nr.M[int](7)\n", "BASHPP-EGENERIC-ARITY: M is not generic; got 1 type argument(s)"},
		{"wrong type argument count", "type R int\nfunc (r R) M[T any](v T) T {\n return v\n}\nvar r R = 1\nr.M[int,string](7)\n", "BASHPP-EGENERIC-ARITY: M expects 1 type argument(s); got 2"},
		{"generic method value needs instantiation", "type R int\nfunc (r R) M[T any](v T) T {\n return v\n}\nvar r R = 1\nf := r.M\n", "BASHPP-EGENERIC-INFER: cannot infer type arguments for M"},
		{"constraint violation", "type R int\nfunc (r R) M[T comparable](v T) T {\n return v\n}\nvar r R = 1\nvar xs []int = []int{1}\nr.M(xs)\n", "BASHPP-EGENERIC-CONSTRAINT: []int does not satisfy constraint for T in M"},
		{"generic method cannot implement an interface", "type R int\nfunc (r R) M[T any](v T) T {\n return v\n}\ntype I interface { M(v int) int }\nvar r R = 1\nvar i I = r\n", "BASHPP-EINTERFACE-GENERIC: R method M declares type parameters and cannot implement an interface method"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, diagnostic, status := genericMethodOracle(t, tc.source)
			t.Logf("interpreter stdout=%q stderr=%q status=%d", out, diagnostic, status)
			if out != "" || diagnostic != tc.want+"\n" || status != 2 {
				t.Fatalf("public diagnostic changed")
			}
			result, err := lower.Compile(parse(t, tc.source, "input.bpp"), lower.Options{Origin: "input.bpp"})
			if result != nil || err == nil {
				t.Fatalf("invalid source produced result=%v err=%v", result, err)
			}
			var list lower.ErrorList
			if !errors.As(err, &list) {
				t.Fatalf("unstructured rejection: %v", err)
			}
			var rendered strings.Builder
			for _, d := range list {
				rendered.WriteString(d.Error())
				rendered.WriteByte('\n')
			}
			if rendered.String() != diagnostic {
				t.Fatalf("compiled diagnostic=%q interpreter=%q", rendered.String(), diagnostic)
			}
		})
	}
}

// Additional controls distinguish result-only instantiation and the marked
// runtime wrapper from ordinary method calls. Their source behavior is checked
// before the native artifact, without borrowing a helper-emitter result.
func TestGenericMethodEntryControls(t *testing.T) {
	cases := []struct {
		name, source, wantOut, wantErr string
		wantStatus                     int
	}{
		{"result_only_explicit", "type R int\nfunc (r R) Zero[T ~int]() T {\n return 0\n}\nvar r R = 1\nx := r.Zero[int]()\necho \"$x\"\n", "0\n", "", 0},
		{"marked_result_only_explicit", "type R int\nagentic func (r R) Zero[T ~int]() T {\n return 0\n}\nvar r R = 1\nagentic { x := r.Zero[int](); echo \"$x\"; }\n", "0\n", "", 0},
		{"marked_allowed", "type R int\nagentic func (r R) M[T any](v T) T {\n echo reached\n return v\n}\nvar r R = 1\nagentic { r.M[int](7); }\n", "reached\n", "", 0},
		{"marked_denied", "type R int\nagentic func (r R) M[T any](v T) T {\n echo reached\n return v\n}\nvar r R = 1\nr.M[int](7)\n", "", "M: agentic action requires an explicit agentic { ...; } scope\n", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, diagnostic, status := genericMethodOracle(t, tc.source)
			t.Logf("interpreter stdout=%q stderr=%q status=%d", out, diagnostic, status)
			if out != tc.wantOut || diagnostic != tc.wantErr || status != tc.wantStatus {
				t.Fatalf("source contract differs from control")
			}
			execute(t, compile(t, tc.source))
		})
	}
}
