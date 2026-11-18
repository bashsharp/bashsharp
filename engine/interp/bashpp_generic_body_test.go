// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp_test

import (
	"testing"

	"github.com/go-quicktest/qt"
)

// A type parameter is in scope for the WHOLE function, not only its signature.
// Go 1.27's own oracle for the reported source
//
//	type R struct{}
//	func (r R) Zero[T any]() T { var zero T; return zero }
//	var r R
//	zero := r.Zero[int]()
//
// prints 0. Before the body-local repair the interpreter printed `zero` — the
// literal name — after reporting `undefined type: T`, because the declaration
// recognizers never see the enclosing parameter list and so parse a body use
// of `T` as an ordinary named type rather than a type parameter use.
// errString renders a run error for comparison, so a case that asserts a
// FAILING status states the status it expects rather than only that something
// went wrong.
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func TestBashPPGenericBodyLocalTypeParams(t *testing.T) {
	tests := []struct{ name, src, want string }{
		{
			"reported source: result-only zero value through a body local",
			"type R struct{}\nfunc (r R) Zero[T any]() T {\n var zero T\n return zero\n}\nvar r R\nzero := r.Zero[int]()\nprintf '%s\\n' \"$zero\"\n",
			"0\n",
		},
		{
			"the same body instantiated at string yields the string zero value",
			"type R struct{}\nfunc (r R) Zero[T any]() T {\n var zero T\n return zero\n}\nvar r R\nzero := r.Zero[string]()\nprintf '[%s]\\n' \"$zero\"\n",
			"[]\n",
		},
		{
			"body local on a defined receiver",
			"type R int\nfunc (r R) Zero[T any]() T {\n var zero T\n return zero\n}\nvar r R = 1\nx := r.Zero[int]()\nprintf '%s\\n' \"$x\"\n",
			"0\n",
		},
		{
			"body local on a pointer receiver",
			"type R int\nfunc (p *R) Zero[T any]() T {\n var zero T\n return zero\n}\nvar r R = 1\nx := r.Zero[int]()\nprintf '%s\\n' \"$x\"\n",
			"0\n",
		},
		{
			"body local through a method expression",
			"type R int\nfunc (r R) Zero[T any]() T {\n var zero T\n return zero\n}\nvar r R = 1\nx := R.Zero[int](r)\nprintf '%s\\n' \"$x\"\n",
			"0\n",
		},
		{
			"body local under inferred type arguments",
			"type R int\nfunc (r R) Echo[T any](v T) T {\n var zero T\n printf 'zero=[%s]\\n' \"$zero\"\n return v\n}\nvar r R = 1\nx := r.Echo(hi)\nprintf '%s\\n' \"$x\"\n",
			"zero=[]\nhi\n",
		},
		{
			"body local in a plain generic function",
			"func Zero[T any]() T {\n var zero T\n return zero\n}\nx := Zero[int]()\nprintf '%s\\n' \"$x\"\n",
			"0\n",
		},
		{
			"body local reassigned before the return",
			"func Zero[T any]() T {\n var zero T\n zero=7\n return zero\n}\nx := Zero[int]()\nprintf '%s\\n' \"$x\"\n",
			"7\n",
		},
		{
			"body local declared inside a nested block",
			"func f[T any]() {\n if true; then\n  var z T\n  printf 'if=%s\\n' z\n fi\n for i in 1; do\n  var w T\n  printf 'for=%s\\n' w\n done\n}\nf[int]()\n",
			"if=0\nfor=0\n",
		},
		{
			"the frame's parameter is forwarded as another call's type argument",
			"func show[U any]() {\n var z U\n printf 'z=[%s]\\n' z\n}\nfunc f[T any]() {\n show[T]()\n}\nf[int]()\nf[string]()\n",
			"z=[0]\nz=[]\n",
		},
		{
			"forwarded as the type argument of a call in a short declaration",
			"func id[U any](v U) U {\n return v\n}\nfunc f[T any](v T) {\n x := id[T]($v)\n printf 'x=%s\\n' \"$x\"\n}\nf[int](6)\n",
			"x=6\n",
		},
		{
			"const group inside a generic body",
			"func f[T any]() {\n const (\n  a T = 3\n )\n printf '%s\\n' \"$a\"\n}\nf[int]()\n",
			"3\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, stderr, err := runBashSharpCall(t, tc.src)
			qt.Assert(t, qt.IsNil(err))
			qt.Assert(t, qt.Equals(stderr, ""))
			qt.Assert(t, qt.Equals(out, tc.want))
		})
	}
}

// Substitution walks the whole type tree, so a parameter nested inside a
// collection, a pointer, an array or a generic named type resolves to the same
// depth as the bare name does. Each case is stated against the CONCRETE
// program it must become: the acceptance question is not "does it print
// something", it is "is `[]T` at T=int indistinguishable from `[]int`".
func TestBashPPGenericBodyNestedCollectionTypeParams(t *testing.T) {
	tests := []struct{ name, generic, concrete string }{
		{
			"slice literal element",
			"func f[T any]() {\n xs := []T{4, 5}\n printf '%s:%s\\n' xs[0] xs[1]\n}\nf[int]()\n",
			"func f() {\n xs := []int{4, 5}\n printf '%s:%s\\n' xs[0] xs[1]\n}\nf()\n",
		},
		{
			"map literal value",
			"func f[T any]() {\n m := map[string]T{\"a\": 1}\n printf '%s\\n' m[\"a\"]\n}\nf[int]()\n",
			"func f() {\n m := map[string]int{\"a\": 1}\n printf '%s\\n' m[\"a\"]\n}\nf()\n",
		},
		{
			"map of slices",
			"func f[T any]() {\n m := map[string][]T{\"a\": {1, 2}}\n printf '%s\\n' m[\"a\"][1]\n}\nf[int]()\n",
			"func f() {\n m := map[string][]int{\"a\": {1, 2}}\n printf '%s\\n' m[\"a\"][1]\n}\nf()\n",
		},
		{
			"declared nested collections, pointer and array",
			"func f[T any]() {\n var m map[string][]T\n var xs [][]T\n var p *T\n var arr [2]T\n printf 'ok\\n'\n}\nf[int]()\n",
			"func f() {\n var m map[string][]int\n var xs [][]int\n var p *int\n var arr [2]int\n printf 'ok\\n'\n}\nf()\n",
		},
		{
			"generic named type instantiated at the parameter",
			"type Box[T any] struct { Value T }\nfunc f[T any]() {\n var b Box[T]\n printf '%s\\n' b.Value\n}\nf[int]()\n",
			"type Box[T any] struct { Value T }\nfunc f() {\n var b Box[int]\n printf '%s\\n' b.Value\n}\nf()\n",
		},
		{
			"composite literal of a generic named type",
			"type Box[T any] struct { Value T }\nfunc f[T any]() {\n b := Box[T]{Value: 4}\n printf '%s\\n' b.Value\n}\nf[int]()\n",
			"type Box[T any] struct { Value T }\nfunc f() {\n b := Box[int]{Value: 4}\n printf '%s\\n' b.Value\n}\nf()\n",
		},
		{
			"struct-typed parameter zero value",
			"type P struct { X int; Y string }\nfunc f[T any]() T {\n var zero T\n return zero\n}\nz := f[P]()\nprintf '%s\\n' \"$z\"\n",
			"type P struct { X int; Y string }\nfunc f() P {\n var zero P\n return zero\n}\nz := f()\nprintf '%s\\n' \"$z\"\n",
		},
		{
			"allocation of the parameter type",
			"func f[T any]() {\n p := new(T)\n printf 'ok\\n'\n}\nf[int]()\n",
			"func f() {\n p := new(int)\n printf 'ok\\n'\n}\nf()\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			wantOut, wantErr, wantRunErr := runBashSharpCall(t, tc.concrete)
			qt.Assert(t, qt.IsNil(wantRunErr))
			qt.Assert(t, qt.Equals(wantErr, ""))
			gotOut, gotErr, gotRunErr := runBashSharpCall(t, tc.generic)
			qt.Assert(t, qt.IsNil(gotRunErr))
			qt.Assert(t, qt.Equals(gotErr, ""))
			qt.Assert(t, qt.Equals(gotOut, wantOut))
		})
	}
}

// The bindings belong to the FRAME, not to the runner's type namespace. Every
// case here would pass just as well if `T` were installed globally on the
// first call, so each one names the leak it exists to rule out.
func TestBashPPGenericBodyTypeParamScopeIsPerCall(t *testing.T) {
	t.Run("no binding survives the call at top level", func(t *testing.T) {
		out, stderr, err := runBashSharpCall(t,
			"func Zero[T any]() T {\n var zero T\n return zero\n}\nx := Zero[int]()\nprintf '%s\\n' \"$x\"\nvar leaked T\n")
		// Status 2 is the ordinary undefined-type exit; what matters here is
		// that the name is undefined again once the frame is gone.
		qt.Assert(t, qt.Equals(errString(err), "exit status 2"))
		qt.Assert(t, qt.Equals(out, "0\n"))
		qt.Assert(t, qt.Equals(stderr, "undefined type: T\n"))
	})
	t.Run("an ordinary callee does not inherit its caller's parameter", func(t *testing.T) {
		out, stderr, err := runBashSharpCall(t,
			"func inner() {\n var leaked T\n printf 'inner\\n'\n}\nfunc Zero[T any]() T {\n inner()\n var zero T\n return zero\n}\nx := Zero[int]()\nprintf '%s\\n' \"$x\"\n")
		qt.Assert(t, qt.IsNil(err))
		qt.Assert(t, qt.Equals(out, "inner\n0\n"))
		qt.Assert(t, qt.Equals(stderr, "undefined type: T\n"))
	})
	t.Run("two instantiations of one function do not see each other", func(t *testing.T) {
		out, stderr, err := runBashSharpCall(t,
			"func Zero[T any]() T {\n var zero T\n return zero\n}\na := Zero[int]()\nb := Zero[string]()\nc := Zero[int]()\nprintf '[%s][%s][%s]\\n' \"$a\" \"$b\" \"$c\"\n")
		qt.Assert(t, qt.IsNil(err))
		qt.Assert(t, qt.Equals(stderr, ""))
		qt.Assert(t, qt.Equals(out, "[0][][0]\n"))
	})
	t.Run("a nested generic call restores its caller's binding", func(t *testing.T) {
		out, stderr, err := runBashSharpCall(t,
			"func Inner[U any]() U {\n var z U\n return z\n}\nfunc Outer[T any]() T {\n s := Inner[string]()\n printf 'inner=[%s]\\n' \"$s\"\n var zero T\n return zero\n}\nx := Outer[int]()\nprintf 'outer=%s\\n' \"$x\"\n")
		qt.Assert(t, qt.IsNil(err))
		qt.Assert(t, qt.Equals(stderr, ""))
		qt.Assert(t, qt.Equals(out, "inner=[]\nouter=0\n"))
	})
	t.Run("recursion rebinds per frame", func(t *testing.T) {
		out, stderr, err := runBashSharpCall(t,
			"func f[T any](n int) {\n var z T\n printf 'n=%s z=[%s]\\n' \"$n\" z\n if [ \"$n\" -gt 0 ]; then\n  f[string]($((n-1)))\n fi\n}\nf[int](1)\n")
		qt.Assert(t, qt.IsNil(err))
		qt.Assert(t, qt.Equals(stderr, ""))
		qt.Assert(t, qt.Equals(out, "n=1 z=[0]\nn=0 z=[]\n"))
	})
	t.Run("receiver and method parameter scopes stay independent", func(t *testing.T) {
		out, stderr, err := runBashSharpCall(t,
			"type Box[T any] struct { Value T }\nfunc (b Box[T]) Pair[U any]() {\n var t T\n var u U\n printf '[%s][%s]\\n' t u\n}\nvar b Box[int] = Box[int]{Value: 1}\nb.Pair[string]()\nb.Pair[int]()\n")
		qt.Assert(t, qt.IsNil(err))
		qt.Assert(t, qt.Equals(stderr, ""))
		qt.Assert(t, qt.Equals(out, "[0][]\n[0][0]\n"))
	})
	t.Run("a closure keeps the instantiation it was created in", func(t *testing.T) {
		out, stderr, err := runBashSharpCall(t,
			"func f[T any]() {\n g := func() {\n  var z T\n  printf 'lit=[%s]\\n' z\n }\n g()\n}\nf[int]()\nf[string]()\n")
		qt.Assert(t, qt.IsNil(err))
		qt.Assert(t, qt.Equals(stderr, ""))
		qt.Assert(t, qt.Equals(out, "lit=[0]\nlit=[]\n"))
	})
}

// Function locals in NON-generic code must be untouched by the repair: the
// substitution is a no-op when no parameter is bound, and a body-local type
// that genuinely is undefined must still say so.
func TestBashPPGenericBodyNonGenericLocalsUnaffected(t *testing.T) {
	t.Run("ordinary function locals", func(t *testing.T) {
		out, stderr, err := runBashSharpCall(t,
			"type P struct { X int }\nfunc f() {\n var n int\n var s string\n var xs []int\n var m map[string][]int\n var p P\n n=3\n printf '%s:[%s]:%s\\n' \"$n\" \"$s\" p.X\n}\nf()\n")
		qt.Assert(t, qt.IsNil(err))
		qt.Assert(t, qt.Equals(stderr, ""))
		qt.Assert(t, qt.Equals(out, "3:[]:0\n"))
	})
	t.Run("an undefined body-local type is still undefined", func(t *testing.T) {
		_, stderr, err := runBashSharpCall(t, "func f() {\n var v Missing\n}\nf()\n")
		qt.Assert(t, qt.Equals(errString(err), "exit status 2"))
		qt.Assert(t, qt.Equals(stderr, "undefined type: Missing\n"))
	})
	t.Run("an undefined body-local type inside a generic body is still undefined", func(t *testing.T) {
		_, stderr, err := runBashSharpCall(t, "func f[T any]() {\n var t T\n var v Missing\n}\nf[int]()\n")
		qt.Assert(t, qt.Equals(errString(err), "exit status 2"))
		qt.Assert(t, qt.Equals(stderr, "undefined type: Missing\n"))
	})
	t.Run("a script type shadowed by a same-named parameter resolves to the argument", func(t *testing.T) {
		// Go's scoping rule: the type parameter shadows the package-level type
		// for the whole function, so the body local is the ARGUMENT's zero
		// value, not the shadowed type's.
		out, stderr, err := runBashSharpCall(t,
			"type T struct { X int }\nfunc f[T any]() T {\n var zero T\n return zero\n}\nx := f[string]()\nprintf '[%s]\\n' \"$x\"\nvar outer T\nprintf '%s\\n' outer.X\n")
		qt.Assert(t, qt.IsNil(err))
		qt.Assert(t, qt.Equals(stderr, ""))
		qt.Assert(t, qt.Equals(out, "[]\n0\n"))
	})
}
