// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"fmt"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// goSourceCallableDeclValue is the closure a `var` declaration's initializer
// denotes when it spells a function value by name — a package function, a
// bound method value, a method expression, a generic instantiation's method
// — rather than by a literal. Only the literal form bound a closure before;
// the others left the variable holding the initializer's text, so a later
// call through the variable found no function and fell through to the
// dependency dispatch (`unknown imported symbol or method: fn`). A
// non-callable initializer reports false and keeps the scalar path.
func (r *Runner) goSourceCallableDeclValue(expr syntax.BashPPExpr) (*bashPPCell, bool, error) {
	switch x := expr.(type) {
	case *syntax.BashPPIdent, *syntax.BashPPSelectorExpr, *syntax.BashPPParenExpr:
	case *syntax.BashPPCall:
		fn, ok := r.goSourceFuncValueCallee(x)
		if !ok {
			return nil, false, nil
		}
		results := bashppResultTypeExprs(fn.results())
		if len(results) != 1 {
			return nil, false, nil
		}
		if _, ok := r.bashPPUnderlyingType(results[0]).(*syntax.BashPPFuncType); !ok {
			return nil, false, nil
		}
		cells, err := r.goSourceCallResultCells(x, fn)
		if err != nil {
			return nil, true, err
		}
		if len(cells) != 1 || cells[0] == nil {
			return nil, true, fmt.Errorf("gosource: callable initializer produced an invalid result")
		}
		cell := cells[0]
		if cell.declType == nil {
			cell.declType = r.bashPPBindTypeExpr(results[0])
		}
		if r.bashPPFuncTypedCell(cell) {
			return cell, true, nil
		}
		return nil, true, fmt.Errorf("gosource: callable initializer produced a non-function result")
	default:
		return nil, false, nil
	}
	cell, handled, err := r.goSourceCallableCell(expr)
	if !handled || err != nil {
		return nil, handled, err
	}
	if cell == nil {
		return nil, true, fmt.Errorf("gosource: callable initializer produced an invalid result")
	}
	if _, closure := r.bashPPClosure(cell.vr.Str); !closure {
		return nil, false, nil
	}
	return cell, true, nil
}
