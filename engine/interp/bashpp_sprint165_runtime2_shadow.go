package interp

// Sprint: #165; Story: #99; Story-ID: ca559d7ee23d
//
// A closure declared in a block shadows a same-named package function.
//
// `func f() { f := func() {…}; f() }` is Go's ordinary lexical rule: the
// innermost declaration of `f` is the local closure, and the call runs it.
// The callee lookup consulted the package-level function table first, so
// the call re-entered the package's `f` — an unbounded recursion the Go
// program never spells (closure.go's `f` and `h` overflow the native stack
// this way). Only a block-scoped binding that holds a closure shadows: a
// package-level variable can never share a package function's name (one
// scope, one declaration), so any such cell is a local of an enclosing
// block, and a cell holding anything but a function value is not a callee.

import "github.com/bashsharp/bashsharp/engine/expand"

// bashPPScopedClosureCallee answers the closure a block-scoped binding of
// name holds, when one is in scope.
func (r *Runner) bashPPScopedClosureCallee(name string) (*bashPPFunc, bool) {
	if r.bashPPScope == nil {
		return nil, false
	}
	vr := r.bashPPScope.lookup(name).viewVar()
	if vr.Kind != expand.String {
		return nil, false
	}
	return r.bashPPClosure(vr.Str)
}
