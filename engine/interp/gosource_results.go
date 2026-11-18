package interp

import (
	"context"
	"errors"
	"fmt"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Result parameters have the same zero values and storage as local variables.
// Reuse declaration rather than manufacturing an empty scalar result cell.
func (r *Runner) goSourceDeclareResults(ctx context.Context, fields []*syntax.BashPPField) bool {
	for _, field := range fields {
		for _, name := range field.Names {
			if name.Value == "_" {
				continue
			}
			r.bashPPDeclare(ctx, &syntax.BashPPDecl{Site: syntax.StartVar, Kw: &syntax.Lit{Value: "var", ValuePos: name.Pos()}, Name: name, DeclType: field.FieldType, DeclTypeExpr: field.FieldTypeExpr})
			if !r.exit.ok() || r.bashPPPanicking() {
				return false
			}
		}
	}
	return true
}

func (r *Runner) goSourceNativeAssignCall(ctx context.Context, assign *syntax.BashPPAssign) {
	if cells, handled := r.goSourceErrorsAsTypeCells(assign.Call); handled {
		if len(cells) != len(assign.Names) {
			r.exit.fatal(fmt.Errorf("%sBASHPP-EASSIGN-ARITY: %d variables but %d native results", r.bashErrPrefix(assign.Eq), len(assign.Names), len(cells)))
			return
		}
		r.bashPPCommitTupleAssign(assign, cells)
		return
	}
	values, err := r.bashPPBridgeCall(ctx, assign.Call)
	if err != nil {
		if !errors.Is(err, errBashPPScalarInterrupted) && !r.bashPPPanicking() {
			r.exit.fatal(err)
		}
		return
	}
	if r.exit.exiting || r.exit.fatalExit || r.exit.err != nil || r.bashPPPanicking() {
		return
	}
	if len(values) != len(assign.Names) {
		r.exit.fatal(fmt.Errorf("%sBASHPP-EASSIGN-ARITY: %d variables but %d native results", r.bashErrPrefix(assign.Eq), len(assign.Names), len(values)))
		return
	}
	cells := make([]*bashPPCell, len(values))
	for i, value := range values {
		var expected syntax.BashPPTypeExpr
		if target := r.bashPPScope.lookup(assign.Names[i].Value).view(); target != nil {
			expected = target.declType
		} else if i < len(assign.Call.ResultTypes) {
			expected = assign.Call.ResultTypes[i]
		}
		cells[i] = r.goSourceNativeTypedResultCell(value, expected)
	}
	r.bashPPCommitTupleAssign(assign, cells)
}

// goSourceNativeTypedResultCell restores the checked static type around the
// dependency's dynamic transport value. Reflection may hand the worker a
// concrete result even when the called signature declares an interface; that
// distinction belongs to every expression path, not just assignments.
func (r *Runner) goSourceNativeTypedResultCell(value bashPPBridgeValue, expected syntax.BashPPTypeExpr) *bashPPCell {
	if _, iface := r.bashPPInterfaceType(expected); !iface {
		return r.goSourceNativeValueCell(value)
	}
	value.Interface = r.bashPPBridgeTypeIdentity(expected)
	cell := r.goSourceNativeValueCell(value)
	cell.declType = expected
	cell.typeName = bashPPNamedTypeBase(expected)
	return cell
}

func (r *Runner) goSourceNativeCallResultCells(call *syntax.BashPPCall, values []bashPPBridgeValue) []*bashPPCell {
	cells := make([]*bashPPCell, len(values))
	for i, value := range values {
		var expected syntax.BashPPTypeExpr
		if call != nil && i < len(call.ResultTypes) {
			expected = call.ResultTypes[i]
		}
		cells[i] = r.goSourceNativeTypedResultCell(value, expected)
	}
	return cells
}

func (r *Runner) goSourceErrorsAsTypeCells(call *syntax.BashPPCall) ([]*bashPPCell, bool) {
	if call == nil || len(call.TypeArgs) != 1 || len(call.ArgExprs) != 1 {
		return nil, false
	}
	if len(call.Fun) != 2 || call.Fun[1].Value != "AsType" || r.bashPPImports[call.Fun[0].Value] != "errors" {
		return nil, false
	}
	assert := &syntax.BashPPTypeAssertExpr{X: call.ArgExprs[0], Assert: call.TypeArgs[0].ArgType}
	values, source, err := r.bashPPTypeAssert(assert, true)
	if err != nil {
		r.exit.fatal(err)
		return nil, true
	}
	if len(values) != 2 || source == nil {
		r.exit.fatal(fmt.Errorf("gosource: errors.AsType returned invalid result shape"))
		return nil, true
	}
	value := bashPPCopyAssignmentCell(source)
	ok := &bashPPCell{vr: expand.Variable{Set: true, Kind: expand.String, Str: values[1]}, declType: &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "bool"}}, typeName: "bool"}
	return []*bashPPCell{value, ok}, true
}
