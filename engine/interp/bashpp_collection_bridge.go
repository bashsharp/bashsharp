// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"fmt"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// A predeclared call that produces a value — `append(xs, 1)`, `copy(dst, src)`,
// `make([]int, 2)`, `new(T)` — is implemented over bashPPCell rather than as a
// callable, so the scalar evaluator has nothing to look up for it and reports
// "undefined callable append". That stays invisible while the result is bound
// by `xs = append(xs, 1)`, because the statement paths in bashpp_p1.go and
// bashpp_readonly.go dispatch the builtin themselves. It surfaces the moment
// the call appears as an argument to a dependency, which is how the Tour writes
// it: `fmt.Println(append(s[:0], "a"))`, `fmt.Println(copy(dst, src), src)`.
//
// bashPPValueBuiltinBridge runs such a call once, where the bridge needs its
// value, and answers with the crossed value whatever its shape: `append` and
// `make` yield a collection, `copy` an int, `new` a pointer. Running it exactly
// once matters — these calls mutate (`copy` writes into its destination), so a
// hook that probed for a collection and then re-ran for a scalar would copy
// twice.
//
// It claims only the value-producing spellings. `len` and `cap` already have a
// scalar spelling and keep it, along with the diagnostics the scalar path owns,
// and the statement-only forms (`clear`, `delete`, `print`, `println`) have no
// value to bridge. A name the session declared itself is never claimed:
// bashPPPredeclaredCall answers only where nothing of that name was declared,
// so a program's own `func copy(...)` shadows the predeclared one as in Go.
func (r *Runner) bashPPValueBuiltinBridge(expr syntax.BashPPExpr) (bashPPBridgeValue, bool, error) {
	call, ok := expr.(*syntax.BashPPCall)
	if !ok || !r.bashPPEnabled() || r.PosixMode() {
		return bashPPBridgeValue{}, false, nil
	}
	name := bashPPPredeclaredCall(call)
	switch name {
	case "append", "copy", "make", "new", "min", "max":
	default:
		return bashPPBridgeValue{}, false, nil
	}
	cell, produced := r.bashPPRunValueBuiltin(name, call)
	if !produced || cell == nil {
		// bashPPRunValueBuiltin has already reported the failure and set the
		// exit status. The call is still claimed, so that its own diagnostic
		// stands rather than being replaced by the scalar evaluator's less
		// specific "undefined callable".
		return bashPPBridgeValue{}, true, fmt.Errorf("gosource: %s produced no value", name)
	}
	if cell.valueMeta != nil {
		bridged, err := r.bashPPBridgeCollection(cell.vr.Obj, cell.valueMeta, cell.valueMeta.typ)
		return bridged, true, err
	}
	// Keep the result cell's declared scalar identity. Re-parsing its storage
	// string loses both complex kind and defined scalar types, so append results
	// used as dependency arguments would expose complex128 as string and Recv as
	// its underlying string.
	bridged, err := r.bashPPBridgeCell(cell)
	return bridged, true, err
}
