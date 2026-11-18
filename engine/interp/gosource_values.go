package interp

import (
	"context"
	"errors"
	"fmt"
	"go/constant"
	"strings"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func goSourceNativeValueCell(value bashPPBridgeValue) *bashPPCell {
	if value.localCell != nil {
		return goSourceLocalReflectCell(value.localCell)
	}
	if scalar, err := value.scalar(); err == nil {
		cell := &bashPPCell{vr: expand.Variable{Set: true, Kind: expand.String, Str: bashPPScalarStorageString(scalar)}, scalarKind: scalar.kind(), negativeZero: scalar.negativeZero, nonFinite: scalar.nonFinite, hasNonFinite: scalar.hasNonFinite, nonFiniteComplex: scalar.nonFiniteComplex, hasNonFiniteComplex: scalar.hasNonFiniteComplex, typeName: scalar.typ, declType: &syntax.BashPPNamedType{Name: &syntax.Lit{Value: scalar.typ}}}
		if value.Interface != "" {
			// The payload is the dynamic value at its own scalar type; only
			// the interface cell carries the static interface type.
			payload := bashPPCopyAssignmentCell(cell)
			cell.declType = &syntax.BashPPNamedType{Name: &syntax.Lit{Value: value.Interface}}
			cell.interfaceValue = &bashPPInterfaceValue{
				nilIface: bashPPBridgeValueNilInterface(value),
				cell:     payload,
				dynamic:  bashPPBridgeValueDynamicType(value),
			}
		}
		return cell
	}
	cell := &bashPPCell{vr: expand.NewObject(&value), typeName: value.Type}
	if value.Interface != "" {
		cell.declType = &syntax.BashPPNamedType{Name: &syntax.Lit{Value: value.Interface}}
		dynamic := bashPPBridgeValueDynamicType(value)
		// The payload is the dynamic value itself, not the interface: once
		// asserted out, a typed nil pointer compares equal to nil, which the
		// dependency denies for a value still marked as an interface.
		// bashPPBridgeCell restores the marker whenever the interface cell
		// crosses back.
		concrete := value
		concrete.Interface = ""
		payload := &bashPPCell{vr: expand.NewObject(&concrete), typeName: value.Type, declType: dynamic}
		cell.interfaceValue = &bashPPInterfaceValue{
			nilIface: bashPPBridgeValueNilInterface(value),
			cell:     payload,
			dynamic:  dynamic,
		}
	}
	return cell
}

func (r *Runner) goSourceNativeValueCell(value bashPPBridgeValue) *bashPPCell {
	cell := goSourceNativeValueCell(value)
	if cell.interfaceValue == nil {
		return cell
	}
	dynamic, ok := cell.interfaceValue.dynamic.(*syntax.BashPPCollectionType)
	if !ok || dynamic.Kind != "array" {
		return cell
	}
	inner, meta, materialized, err := r.goSourceNativeSequenceContents(&value, dynamic)
	if materialized && err == nil {
		bashPPStoreCellValue(cell.interfaceValue.cell, inner, meta)
	}
	return cell
}
func (r *Runner) goSourceCallableCell(expr syntax.BashPPExpr) (*bashPPCell, bool, error) {
	if !r.bashPPGoSource || expr == nil {
		return nil, false, nil
	}
	switch x := expr.(type) {
	case *syntax.BashPPParenExpr:
		return r.goSourceCallableCell(x.X)
	case *syntax.BashPPFuncLit:
		_, v := r.bashPPMakeClosure(x)
		return &bashPPCell{vr: v, declType: r.bashPPBindTypeExpr(bashPPFuncLitType(x))}, true, nil
	case *syntax.BashPPIdent:
		if cell := r.bashPPScope.lookup(x.Name.Value); cell != nil {
			if _, ok := r.bashPPClosure(cell.viewVar().Str); ok {
				return bashPPCopyAssignmentCell(cell), true, nil
			}
			// A non-callable local binding shadows the package function too.
			return nil, false, nil
		}
		if fn := r.bashPPFuncs[x.Name.Value]; fn != nil {
			return &bashPPCell{vr: r.bashPPStoreFunc(fn)}, true, nil
		}
	case *syntax.BashPPSelectorExpr:
		if x.MethodValue && !r.bashPPNativeExpr(x.X) {
			cell, err := r.goSourceLocalMethodValue(x)
			return cell, true, err
		}
		if cell, handled, err := r.goSourceMethodExprCell(x); handled {
			return cell, true, err
		}
		if x.FuncType == nil {
			return nil, false, nil
		}
		if id, ok := x.X.(*syntax.BashPPIdent); ok {
			if _, imported := r.bashPPImports[id.Name.Value]; imported {
				v, err := r.bashPPBridgeExpr(x)
				if err != nil {
					return nil, true, err
				}
				fn := &bashPPFunc{native: &v, lit: &syntax.BashPPFuncLit{Params: x.FuncType.Params, Results: x.FuncType.Results}}
				return &bashPPCell{vr: r.bashPPStoreFunc(fn), declType: x.FuncType}, true, nil
			}
		}
	}
	return nil, false, nil
}
func (r *Runner) goSourceValueCell(expr syntax.BashPPExpr) (*bashPPCell, error) {
	cells, err := r.goSourceValueCells(expr, false)
	if err != nil {
		return nil, err
	}
	return cells[0], nil
}

// goSourceComputedCalleeCell evaluates the value in callee position once.
// The general value path first probes selector/index reads for structured
// metadata and then falls back to scalar evaluation; that is harmless for a
// passive local but replays an effectful receiver or index. A computed callee
// needs the value either way, so materialize the first read directly into the
// same typed cell used by collection reads and native handles.
func (r *Runner) goSourceComputedCalleeCell(expr syntax.BashPPExpr) (*bashPPCell, error) {
	if paren, ok := expr.(*syntax.BashPPParenExpr); ok {
		return r.goSourceComputedCalleeCell(paren.X)
	}
	if cell, handled, err := r.goSourceCallableCell(expr); handled {
		return cell, err
	}
	switch expr.(type) {
	case *syntax.BashPPSelectorExpr, *syntax.BashPPIndexExpr, *syntax.BashPPSliceExpr,
		*syntax.BashPPDerefExpr, *syntax.BashPPTypeAssertExpr:
		value, meta, err := r.bashPPReadExpr(expr)
		if err != nil {
			return nil, err
		}
		return r.goSourceCollectionReadCell(expr, value, meta), nil
	}
	return r.goSourceValueCell(expr)
}

// goSourceValueCells evaluates one expression to its result cells. A call is
// the only expression with more than one: with spread set it contributes
// every result it returns, which is how `f(g())` hands g's results to f;
// without it a call must yield exactly one value. Every other expression is
// one cell.
func (r *Runner) goSourceValueCells(expr syntax.BashPPExpr, spread bool) ([]*bashPPCell, error) {
	one := func(cell *bashPPCell, err error) ([]*bashPPCell, error) {
		if err != nil {
			return nil, err
		}
		return []*bashPPCell{cell}, nil
	}
	if cell, handled, err := r.goSourceNilValueCell(expr); handled {
		return one(cell, err)
	}
	if paren, ok := expr.(*syntax.BashPPParenExpr); ok {
		return r.goSourceValueCells(paren.X, spread)
	}
	if cell, handled, err := r.goSourceChannelValueCell(expr); handled {
		return one(cell, err)
	}
	if cell, handled, err := r.goSourceCollectionBuiltinCell(expr); handled {
		return one(cell, err)
	}
	if call, ok := expr.(*syntax.BashPPCall); ok {
		if conversion, converted := r.bashPPConversionCall(call); converted {
			if cell, err := r.bashPPStructuredArgCell(nil, conversion); cell != nil || err != nil {
				return one(cell, err)
			}
		}
		if cell, handled, err := r.goSourceComplexBuiltinCell(call); handled {
			return one(cell, err)
		}
	}
	if cell, handled, err := r.goSourceCallableCell(expr); handled {
		return one(cell, err)
	}
	if call, ok := expr.(*syntax.BashPPCall); ok {
		if cell, handled, err := r.goSourceBuiltinResult(call); handled {
			return one(cell, err)
		}
		if r.bashPPBridgeHandles(call) {
			values, err := r.bashPPBridgeCall(r.ectx, call)
			if err != nil {
				return nil, err
			}
			if len(values) != 1 && !spread {
				return nil, fmt.Errorf("Go value requires one result")
			}
			return r.goSourceNativeCallResultCells(call, values), nil
		}
		if fn, ok := r.bashPPLookupFunc(call); ok {
			cells, err := r.goSourceCallResultCells(call, fn)
			if err != nil {
				return nil, err
			}
			if len(cells) != 1 && !spread {
				return nil, fmt.Errorf("Go value requires one result")
			}
			return cells, nil
		} else if r.bashPPPanicHalts() || r.exit.code != 0 {
			// The callee lookup itself raised — a method selected on a nil
			// interface — or reported; evaluating the call again below would
			// raise a second panic inside the unwinding of the first.
			return nil, errBashPPScalarInterrupted
		}
	}
	// A field or index read from an interpreter-owned aggregate may carry a
	// typed nil func or channel. Preserve that cell before the native-expression
	// path below: transporting it first keeps the nil payload but drops the
	// aggregate's declared type, leaving a later `value == nil` to fall back to
	// scalar evaluation. StructuredArgCell evaluates the read once and retains
	// both its payload and metadata, including for non-nil aggregate values.
	if cell, err := r.bashPPStructuredArgCell(nil, expr); err != nil || cell != nil {
		return one(cell, err)
	}
	if r.bashPPNativeExpr(expr) {
		value, err := r.bashPPBridgeExpr(expr)
		if err != nil {
			return nil, err
		}
		return one(r.goSourceNativeValueCell(value), nil)
	}
	v, err := r.bashPPEvalScalarExpr(expr)
	if err != nil {
		return nil, err
	}
	cell := &bashPPCell{vr: expand.Variable{Set: true, Kind: expand.String, Str: bashPPScalarStorageString(v)}, scalarKind: v.kind(), negativeZero: v.negativeZero, nonFinite: v.nonFinite, hasNonFinite: v.hasNonFinite, nonFiniteComplex: v.nonFiniteComplex, hasNonFiniteComplex: v.hasNonFiniteComplex, typeName: v.typ}
	if !v.runtime {
		cell.constant, cell.exactScalar = true, v.value
	}
	if v.typ != "" {
		cell.declType, cell.typeName = bashPPScalarNamedType(v.typ)
	}
	// Scalar evaluation keeps the short type name in bashPPScalar for
	// arithmetic, but an instantiated conversion carries more identity than
	// that spelling. Preserve its structured target on the value cell so an
	// interface observes T[int] from the correct local declaration.
	if conversion, ok := expr.(*syntax.BashPPConvertExpr); ok {
		if target := r.bashPPConvertTarget(conversion); target != nil {
			cell.declType = target
		}
	}
	return one(cell, nil)
}

// goSourceComplexBuiltinCell keeps the predeclared complex family on the Go
// value path. Unlike an ordinary scalar call, complex(f()) expands f's two
// results into the builtin's operands.
func (r *Runner) goSourceComplexBuiltinCell(call *syntax.BashPPCall) (*bashPPCell, bool, error) {
	if !r.bashPPGoSource || call == nil || len(call.Fun) != 1 {
		return nil, false, nil
	}
	name := call.Fun[0].Value
	if name != "complex" && name != "real" && name != "imag" {
		return nil, false, nil
	}
	if _, ok := r.bashPPLookupFunc(call); ok {
		return nil, false, nil
	}
	args := make([]bashPPScalar, 0, len(call.ArgExprs))
	for _, expr := range call.ArgExprs {
		values, err := r.goSourceValueCells(expr, len(call.ArgExprs) == 1)
		if err != nil {
			return nil, true, err
		}
		for _, value := range values {
			args = append(args, r.bashPPScalarFromCell(value))
		}
	}
	want := 1
	if name == "complex" {
		want = 2
	}
	if len(args) != want {
		return nil, true, fmt.Errorf("%s requires %d arguments", name, want)
	}
	result, err := r.bashPPComplexBuiltinValues(name, args)
	if err != nil {
		return nil, true, err
	}
	if result.typ == "" {
		if result.kind() == constant.Complex {
			result.typ = "complex128"
		} else {
			result.typ = "float64"
		}
	}
	cell := &bashPPCell{
		vr:                  expand.Variable{Set: true, Kind: expand.String, Str: bashPPScalarStorageString(result)},
		scalarKind:          result.kind(),
		nonFinite:           result.nonFinite,
		hasNonFinite:        result.hasNonFinite,
		nonFiniteComplex:    result.nonFiniteComplex,
		hasNonFiniteComplex: result.hasNonFiniteComplex,
		typeName:            result.typ,
		declType:            &syntax.BashPPNamedType{Name: &syntax.Lit{Value: result.typ}},
	}
	return cell, true, nil
}
func (r *Runner) goSourceValues(exprs []syntax.BashPPExpr) ([]*bashPPCell, bool) {
	cells := make([]*bashPPCell, len(exprs))
	for i, expr := range exprs {
		cell, err := r.goSourceValueCell(expr)
		if err != nil {
			if !errors.Is(err, errBashPPScalarInterrupted) {
				r.errf("%s%v\n", r.bashErrPrefix(expr.Pos()), err)
				r.exit = exitStatus{code: 2}
				r.bashPPShortFailureSeq++
			}
			return nil, false
		}
		cells[i] = bashPPCopyAssignmentCell(cell)
	}
	return cells, true
}
func (r *Runner) goSourceParallelDecl(d *syntax.BashPPShortDecl) {
	if len(d.Lhs) != len(d.RhsExprs) {
		r.errf("Go assignment arity mismatch\n")
		r.exit = exitStatus{code: 2}
		return
	}
	cells, ok := r.goSourceValues(d.RhsExprs)
	if !ok {
		return
	}
	for i, lhs := range d.Lhs {
		if lhs.Value == "_" {
			continue
		}
		r.bashPPDeclareName(lhs.Value, cells[i].vr)
		if target := r.bashPPScope.lookup(lhs.Value); target != nil {
			target.publish(cells[i])
			// A constant RHS keeps exact expression metadata while operands
			// are evaluated, but := always introduces mutable variables.
			target.constant, target.vr.ReadOnly = false, false
			target.exactScalar = nil
		}
	}
}
func (r *Runner) goSourceReturnValues(exprs []syntax.BashPPExpr) {
	cells, ok := r.goSourceValues(exprs)
	if !ok {
		return
	}
	values := make([]string, len(cells))
	for i, c := range cells {
		values[i] = c.vr.String()
	}
	r.bashPPReturn = bashPPReturnState{active: true, values: values, cells: cells}
	r.exit.returning = true
}
func (r *Runner) goSourceInvokeNative(ctx context.Context, fn *bashPPFunc, args []string, cells []*bashPPCell) []string {
	req, err := r.bashPPEvalRequest()
	if err != nil {
		r.exit.fatal(err)
		return nil
	}
	q := bashPPBridgeRequest{Op: "call", Receiver: fn.native}
	for i, arg := range args {
		var cell *bashPPCell
		if i < len(cells) {
			cell = cells[i]
		}
		if cell == nil {
			cell = &bashPPCell{vr: expand.Variable{Set: true, Kind: expand.String, Str: arg}}
		}
		value, err := r.bashPPBridgeCell(cell)
		if err != nil {
			r.exit.fatal(err)
			return nil
		}
		q.Args = append(q.Args, value)
	}
	values, err := r.bashPPNativeRequest(ctx, req, q)
	if err != nil {
		r.exit.fatal(err)
		return nil
	}
	results := make([]string, len(values))
	r.bashPPResultCells = make([]*bashPPCell, len(values))
	for i, value := range values {
		cell := r.goSourceNativeValueCell(value)
		r.bashPPResultCells[i] = cell
		results[i] = cell.vr.String()
	}
	return results
}

// bashPPGoSourceComputedNativeCall dispatches a call whose computed callee
// evaluates to a dependency's function value — `m.Func.Interface().(func(M))(v)`,
// `fs[i]()` where fs holds native handles. The statement and expression callee
// branches resolve only local closures and named cells by themselves; a handle
// reached through a type assertion, index, or any other computed expression has
// no name to look up, so it is evaluated here and invoked on the dependency.
//
// It reports whether it claimed the call. A handle that is not a function
// value, or a callee that does not evaluate to one, is declined so the ordinary
// "computed callee is not a function" diagnostic still applies.
func (r *Runner) bashPPGoSourceComputedNativeCall(ctx context.Context, c *syntax.BashPPCall) bool {
	if !r.bashPPGoSource || c == nil || c.CalleeExpr == nil {
		return false
	}
	fn, ok := r.goSourceComputedNativeFunc(c)
	if !ok {
		return false
	}
	args, ok := r.bashPPCallValues(c, fn)
	if !ok {
		return true
	}
	r.bashPPInvoke(ctx, fn, args)
	return true
}

func (r *Runner) goSourceComputedNativeFunc(c *syntax.BashPPCall) (*bashPPFunc, bool) {
	if !r.bashPPGoSource || c == nil || c.CalleeExpr == nil {
		return nil, false
	}
	cell, err := r.goSourceComputedCalleeCell(c.CalleeExpr)
	if err != nil || cell == nil {
		return nil, false
	}
	return r.goSourceComputedNativeFuncCell(c, cell)
}

// goSourceComputedNativeFuncCell recognizes an already-evaluated computed
// callee. Keeping recognition separate from evaluation lets local closure and
// native-handle dispatch share one read of an effectful selector or index.
func (r *Runner) goSourceComputedNativeFuncCell(c *syntax.BashPPCall, cell *bashPPCell) (*bashPPFunc, bool) {
	if c == nil || cell == nil {
		return nil, false
	}
	value, err := r.bashPPBridgeCell(cell)
	if err != nil {
		return nil, false
	}
	if value.Kind != "handle" || !(value.Function || strings.HasPrefix(value.Type, "func(")) {
		return nil, false
	}
	if fn, ok := r.goSourceLocalReflectFunc(&value); ok {
		return fn, true
	}
	fn := &bashPPFunc{native: &value}
	if sig := bashPPComputedCalleeSignature(c.CalleeExpr, cell); sig != nil {
		fn.lit = &syntax.BashPPFuncLit{Params: sig.Params, Results: sig.Results}
	}
	return fn, true
}

// bashPPComputedCalleeSignature recovers the concrete signature a computed
// callee was asserted or declared to have, so argument binding has the
// parameter types the dependency expects. A type assertion `.(func(...))` spells
// it directly; otherwise the evaluated cell's declared type may carry it.
func bashPPComputedCalleeSignature(expr syntax.BashPPExpr, cell *bashPPCell) *syntax.BashPPFuncType {
	switch x := expr.(type) {
	case *syntax.BashPPParenExpr:
		return bashPPComputedCalleeSignature(x.X, cell)
	case *syntax.BashPPTypeAssertExpr:
		if ft, ok := x.Assert.(*syntax.BashPPFuncType); ok {
			return ft
		}
	}
	if cell != nil {
		if ft, ok := cell.declType.(*syntax.BashPPFuncType); ok {
			return ft
		}
	}
	return nil
}
