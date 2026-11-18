// Copyright (c) 2026, the bash++ authors.
// See LICENSE for licensing information.

package interp

import (
	"fmt"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// bashPPSelectReceiveAssign commits the value selected by a receive clause to
// its ordinary assignment target. Selection has already performed the receive,
// so this deliberately commits the supplied cell rather than evaluating a
// second receive expression.
func (r *Runner) bashPPSelectReceiveAssign(assign *syntax.BashPPAssign, received *bashPPCell, open bool) {
	if assign == nil || received == nil {
		return
	}
	if len(assign.Names) > 0 {
		if len(assign.Names) > 2 {
			r.errf("receive assignment mismatch\n")
			r.exit.code = 2
			return
		}
		values := []*bashPPCell{bashPPCopyAssignmentCell(received)}
		if len(assign.Names) == 2 {
			values = append(values, &bashPPCell{vr: expand.Variable{Set: true, Kind: expand.String, Str: fmt.Sprint(open)}, typeName: "bool"})
		}
		r.bashPPCommitTupleAssign(assign, values)
		return
	}
	if assign.TargetExpr == nil {
		r.errf("BASHPP-ENONADDRESSABLE: operand is not addressable\n")
		r.exit.code = 2
		return
	}
	ptr, err := r.bashPPAddress(assign.TargetExpr)
	if err != nil {
		r.errf("%v\n", err)
		r.exit.code = 2
		return
	}
	if ptr == nil || ptr.target == nil {
		return
	}
	if err := goSourceUnsafeDerefCheck(ptr); err != nil {
		r.errf("%v\n", err)
		r.exit.code = 2
		return
	}
	if ptr.unsafeView() != nil {
		r.errf("BASHPP-EUNSAFE-WRITE: writes through reinterpreted blank views are unsupported\n")
		r.exit.code = 2
		return
	}
	if ptr.unsafeSlice() != nil {
		r.errf("BASHPP-EUNSAFE-WRITE: writes through a reinterpreted slice header are unsupported\n")
		r.exit.code = 2
		return
	}
	candidate := bashPPCopyAssignmentCell(received)
	target := &bashPPCell{declType: ptr.elem}
	r.bashPPPrepareInterfaceAssignment(target, candidate)
	if r.exit.code != 0 {
		return
	}
	if err := r.bashPPValidateReusedShortValue(target, candidate); err != nil {
		r.errf("%v\n", err)
		r.exit.code = 2
		return
	}
	if ptr.target.constant || ptr.target.vr.ReadOnly {
		r.errf("BASHPP-EREADONLY-MUTATION: cannot mutate readonly value through pointer\n")
		r.exit.code = 2
		return
	}
	if len(ptr.path) == 0 {
		declType, typeName := ptr.target.declType, ptr.target.typeName
		ptr.target.publish(candidate)
		ptr.target.declType, ptr.target.typeName = declType, typeName
		return
	}
	parent, parentMeta, _, err := ptr.readParent()
	if err != nil {
		r.errf("%v\n", err)
		r.exit.code = 2
		return
	}
	if parentMeta == nil {
		r.errf("BASHPP-ESELECTOR-TYPE: assignment parent is not a structured value\n")
		r.exit.code = 2
		return
	}
	value := bashPPSelectReceivedValue(candidate)
	last := ptr.path[len(ptr.path)-1]
	if last.field != "" {
		mapping, ok := parent.(map[string]any)
		if !ok {
			r.errf("BASHPP-ESELECTOR-TYPE: assignment parent is not struct storage\n")
			r.exit.code = 2
			return
		}
		bashPPStorageSetField(mapping, parentMeta.mapping, last.field, value, candidate.valueMeta)
		return
	}
	sequence, ok := parent.([]any)
	if !ok || last.index < 0 || last.index >= len(sequence) {
		r.errf("BASHPP-ESELECTOR-TYPE: assignment parent is not collection storage\n")
		r.exit.code = 2
		return
	}
	sequence[last.index] = value
	parentMeta.sequence[last.index] = candidate.valueMeta
}

func bashPPSelectReceivedValue(cell *bashPPCell) any {
	if cell.pointer {
		return cell.pointerValue
	}
	if cell.vr.Kind == expand.Object {
		return cell.vr.Obj
	}
	return bashPPScalarValue(cell.vr.Str)
}

func bashPPSelectAssignReceive(assign *syntax.BashPPAssign) *syntax.BashPPReceive {
	if assign == nil {
		return nil
	}
	if assign.Recv != nil {
		return assign.Recv
	}
	if len(assign.ValueExprs) != 1 {
		return nil
	}
	receive, ok := assign.ValueExprs[0].(*syntax.BashPPUnaryExpr)
	if !ok || receive.Op == nil || receive.Op.Value != "<-" {
		return nil
	}
	return &syntax.BashPPReceive{Arrow: receive.Pos(), ChanExpr: receive.X}
}
