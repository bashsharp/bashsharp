// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp_test

import (
	"strings"
	"testing"

	"github.com/go-quicktest/qt"
)

// Go 1.27 gives a method type parameters of its own, independent of the
// receiver's: `func (r R) M[T any](v T) T` compiles, and `R{}.M[int](7)`
// prints 7. The cases below mirror that oracle program shape for shape —
// explicit instantiation, inference, constraints, and the interaction with a
// generic receiver whose parameters are bound by the receiver VALUE rather
// than by the call.
func TestBashPPIndependentMethodTypeParamsRuntime(t *testing.T) {
	tests := []struct{ name, src, want string }{
		{
			"explicit instantiation on an ordinary receiver",
			"type R int\nfunc (r R) M[T any](v T) T {\n return v\n}\nvar r R = 1\nx := r.M[int](7)\necho \"x=$x\"\n",
			"x=7\n",
		},
		{
			"inference on an ordinary receiver",
			"type R int\nfunc (r R) M[T any](v T) T {\n return v\n}\nvar r R = 1\nx := r.M(hi)\necho \"x=$x\"\n",
			"x=hi\n",
		},
		{
			"constrained method type parameter",
			"type Num interface { ~int }\ntype R int\nfunc (r R) Sum[T Num](a T, b T) T {\n return $((a+b))\n}\nvar r R = 1\nx := r.Sum[int](3, 4)\necho \"x=$x\"\n",
			"x=7\n",
		},
		{
			"constrained method type parameter inferred",
			"type Num interface { ~int }\ntype R int\nfunc (r R) Sum[T Num](a T, b T) T {\n return $((a+b))\n}\nvar r R = 1\nx := r.Sum(3, 4)\necho \"x=$x\"\n",
			"x=7\n",
		},
		{
			"two method type parameters",
			"type R int\nfunc (r R) Pick[T any, U any](a T, b U) T {\n return a\n}\nvar r R = 1\nx := r.Pick[int,string](3, hi)\necho \"x=$x\"\n",
			"x=3\n",
		},
		{
			"generic receiver and method scopes are independent",
			"type Box[T any] struct { Value T }\nfunc (b Box[T]) Pair[U any](u U) U {\n return u\n}\nvar b Box[int] = Box[int]{Value: 1}\nx := b.Pair[string](hey)\necho \"x=$x\"\n",
			"x=hey\n",
		},
		{
			"generic receiver binding survives method instantiation",
			"type Box[T any] struct { Value T }\nfunc (b Box[T]) Take[U any](t T, u U) T {\n return t\n}\nvar b Box[int] = Box[int]{Value: 1}\nx := b.Take[string](4, hey)\necho \"x=$x\"\n",
			"x=4\n",
		},
		{
			"pointer receiver",
			"type R int\nfunc (p *R) M[T any](v T) T {\n return v\n}\nvar r R = 1\nx := r.M[int](5)\necho \"x=$x\"\n",
			"x=5\n",
		},
		{
			"method expression",
			"type R int\nfunc (r R) M[T any](v T) T {\n return v\n}\nvar r R = 1\nx := R.M[int](r, 5)\necho \"x=$x\"\n",
			"x=5\n",
		},
		{
			"non-generic method is unaffected",
			"type R int\nfunc (r R) M(v int) int {\n return v\n}\nvar r R = 1\nx := r.M(5)\necho \"x=$x\"\n",
			"x=5\n",
		},
		{
			"generic receiver method without its own parameters is unaffected",
			"type Box[T any] struct { Value T }\nfunc (b Box[T]) Get(v T) T {\n return v\n}\nvar b Box[int] = Box[int]{Value: 1}\nx := b.Get(9)\necho \"x=$x\"\n",
			"x=9\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			qt.Assert(t, qt.Equals(runBashPPFunc(t, tc.src), tc.want))
		})
	}
}

// The shapes Go 1.27 still rejects stay rejected. Interfaces in particular did
// NOT gain generic methods, so a generic method is not in the method set an
// interface can name.
func TestBashPPIndependentMethodTypeParamsDiagnostics(t *testing.T) {
	tests := []struct{ name, src, want string }{
		{
			"type arguments on a non-generic method",
			"type R int\nfunc (r R) M(v int) int {\n return v\n}\nvar r R = 1\nr.M[int](7)\n",
			"BASHPP-EGENERIC-ARITY: M is not generic; got 1 type argument(s)",
		},
		{
			"wrong type argument count",
			"type R int\nfunc (r R) M[T any](v T) T {\n return v\n}\nvar r R = 1\nr.M[int,string](7)\n",
			"BASHPP-EGENERIC-ARITY: M expects 1 type argument(s); got 2",
		},
		{
			"generic method value needs instantiation",
			"type R int\nfunc (r R) M[T any](v T) T {\n return v\n}\nvar r R = 1\nf := r.M\n",
			"BASHPP-EGENERIC-INFER: cannot infer type arguments for M",
		},
		{
			"constraint violation",
			"type R int\nfunc (r R) M[T comparable](v T) T {\n return v\n}\nvar r R = 1\nvar xs []int = []int{1}\nr.M(xs)\n",
			"BASHPP-EGENERIC-CONSTRAINT: []int does not satisfy constraint for T in M",
		},
		{
			"generic method cannot implement an interface",
			"type R int\nfunc (r R) M[T any](v T) T {\n return v\n}\ntype I interface { M(v int) int }\nvar r R = 1\nvar i I = r\n",
			"BASHPP-EINTERFACE-GENERIC: R method M declares type parameters and cannot implement an interface method",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := runBashPPFunc(t, tc.src)
			if !strings.Contains(got, tc.want) {
				t.Fatalf("output = %q, want to contain %q", got, tc.want)
			}
		})
	}
}
