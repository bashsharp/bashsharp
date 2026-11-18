package interp

import (
	"github.com/bashsharp/bashsharp/engine/syntax"
	"go/token"
)

// A concrete scalar compared with an interface is boxed at its own declared
// type. Going through scalar fallback discards the interface operand and can
// also evaluate a consuming operand twice.
func (r *Runner) goSourceInterfaceScalarComparison(left syntax.BashPPExpr, op token.Token, right syntax.BashPPExpr) (bool, bool, error) {
	if !r.bashPPGoSource {
		return false, false, nil
	}
	classify := func(expr syntax.BashPPExpr) (bool, bool) {
		typ, ok := r.goSourceStaticExprType(expr)
		if !ok {
			return false, false
		}
		if _, ok := r.bashPPInterfaceType(typ); ok {
			return true, false
		}
		named, ok := r.bashPPUnderlyingType(typ).(*syntax.BashPPNamedType)
		return false, ok && named.Name != nil && bashPPBuiltinType(named.Name.Value) && named.Name.Value != "any" && named.Name.Value != "error"
	}
	li, ls := classify(left)
	ri, rs := classify(right)
	if !(li && rs || ls && ri) {
		return false, false, nil
	}
	box := func(expr syntax.BashPPExpr) (bashPPComparableValue, error) {
		cell, err := r.goSourceValueCell(expr)
		if err != nil {
			return bashPPComparableValue{}, err
		}
		iv := cell.interfaceValue
		if iv == nil {
			// Aggregate/native writeback can retain the interface identity on
			// value metadata rather than directly on the binding cell.
			if meta := bashPPCellMeta(cell); meta != nil {
				iv = meta.interfaceValue
			}
		}
		if iv == nil {
			payload, typ, err := r.bashPPInterfaceSourceCell(cell, "comparison operand")
			if err != nil {
				return bashPPComparableValue{}, err
			}
			iv = &bashPPInterfaceValue{cell: payload, dynamic: typ}
		}
		return bashPPComparableValue{value: iv}, nil
	}
	lv, err := box(left)
	if err != nil {
		return false, true, err
	}
	rv, err := box(right)
	if err != nil {
		return false, true, err
	}
	equal, _, err := r.goSourceInterfaceEqual(lv, rv)
	if op == token.NEQ {
		equal = !equal
	}
	return equal, true, err
}
