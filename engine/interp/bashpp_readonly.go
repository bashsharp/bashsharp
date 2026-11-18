// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"context"
	"errors"
	"fmt"
	"go/constant"
	"strconv"
	"strings"
	"sync"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// bashPPObjectIdentity is shared by the cells created when an object is
// aliased. Marking any root readonly freezes every path through that object.
type bashPPObjectIdentity struct {
	owner      string
	readonly   bool
	collection *bashPPCollectionMeta
}

type bashPPPathPart struct {
	field string
	index any
	text  string
}

// bashPPWordSourcePrinters reuses printers for bashPPWordSource: the text of a
// Go builtin argument is spelled on every evaluation, and a fresh printer
// allocates its 4 KiB buffer each time. Print resets all printer state.
var bashPPWordSourcePrinters = sync.Pool{New: func() any { return syntax.NewPrinter() }}

func bashPPWordSource(w *syntax.Word) string {
	// A lone literal prints as itself unless the tab writer would act on it.
	if w != nil && len(w.Parts) == 1 {
		if lit, ok := w.Parts[0].(*syntax.Lit); ok && !strings.ContainsAny(lit.Value, "\t\n\v\f\r\xff") {
			return lit.Value
		}
	}
	var b strings.Builder
	p := bashPPWordSourcePrinters.Get().(*syntax.Printer)
	_ = p.Print(&b, w)
	bashPPWordSourcePrinters.Put(p)
	return b.String()
}

func bashPPPathValue(value any, parts []bashPPPathPart) (any, bool) {
	for _, part := range parts {
		switch current := value.(type) {
		case map[string]any:
			key := part.field
			if key == "" {
				key, _ = part.index.(string)
			}
			value, _ = bashPPStorageGet(current, key)
		case []any:
			i, ok := part.index.(int)
			if !ok || i < 0 || i >= len(current) {
				return nil, false
			}
			value = current[i]
		default:
			return nil, false
		}
	}
	return value, true
}

func (r *Runner) bashPPAssign(ctx context.Context, assign *syntax.BashPPAssign) {
	if !r.objectsEnabled() || r.bashPPScope == nil {
		r.errf("bash++ assignment evaluated with extensions disabled\n")
		r.exit = exitStatus{code: 2}
		return
	}
	// `x[i] = v` over int elements has one meaning and no effects beyond the
	// store; see bashpp_fastint.go.
	if assign.Call == nil && assign.Recv == nil && len(assign.Names) == 0 && assign.ValueExpr != nil {
		if index, ok := assign.TargetExpr.(*syntax.BashPPIndexExpr); ok && r.bashPPFastIntIndexAssign(index, assign.ValueExpr) {
			return
		}
	}
	// `p = new(T)`, `ps[i] = new(T)`: the Go front end lowers the allocation
	// as the NewExpr in ValueExpr and also records the call. The allocation
	// is what is assigned; the call spelling would look up `new` as a
	// function and read `T` as a value.
	allocation := false
	if r.bashPPGoSource && assign.Call != nil && bashPPPredeclaredCall(assign.Call) == "new" && r.bashPPFuncs["new"] == nil && r.bashPPScope.lookup("new") == nil {
		_, allocation = assign.ValueExpr.(*syntax.BashPPNewExpr)
	}
	if r.bashPPGoSource && assign.Call != nil {
		rhs := syntax.BashPPExpr(assign.Call)
		if allocation {
			rhs = assign.ValueExpr
		}
		switch assign.TargetExpr.(type) {
		case *syntax.BashPPIndexExpr, *syntax.BashPPSelectorExpr, *syntax.BashPPDerefExpr:
			r.bashPPStructuredAssign(assign.TargetExpr, rhs)
			return
		}
	}
	if len(assign.Names) > 0 {
		if assign.Call != nil && !allocation {
			r.bashPPTupleAssignCall(ctx, assign)
			return
		}
		r.bashPPTupleAssign(assign)
		return
	}
	if assign.Call != nil {
		r.bashPPBuiltinAssign(assign)
		return
	}
	if assign.TargetExpr != nil {
		if deref, ok := assign.TargetExpr.(*syntax.BashPPDerefExpr); ok {
			r.bashPPDerefAssign(deref, assign.ValueExpr)
			return
		}
		r.bashPPStructuredAssign(assign.TargetExpr, assign.ValueExpr)
		return
	}
	target := bashPPWordSource(assign.Target)
	cell := r.bashPPScope.lookup(target)
	binding := cell.view()
	if syntax.BashPPValidIdent(target) && binding != nil && binding.pointer && assign.ValueExpr != nil {
		value, meta, err := r.bashPPEvalTypedValue(assign.ValueExpr, binding.declType)
		if errors.Is(err, errBashPPScalarInterrupted) {
			return
		}
		if err != nil {
			r.errf("BASHPP-EASSIGN-MISMATCH: %v\n", err)
			r.exit = exitStatus{code: 2}
			return
		}
		// The RHS above ran guest code, which could have marked the binding
		// readonly, so this guard asks the cell again rather than the snapshot
		// the shape decision was made on.
		if guard := cell.view(); guard.constant || guard.vr.ReadOnly {
			r.errf("BASHPP-EREADONLY-MUTATION: cannot mutate readonly value through pointer\n")
			r.exit = exitStatus{code: 2}
			return
		}
		bashPPStoreCellValue(cell, value, meta)
		return
	}
	// No guest code ran on the way here: the block above returns whenever it
	// evaluates anything, so the shape snapshot is still what the cell holds.
	if !syntax.BashPPValidIdent(target) || binding == nil || binding.object == nil || !binding.object.readonly {
		r.errf("bash++: mutation is only implemented for readonly objects\n")
		r.exit = exitStatus{code: 2}
		return
	}
	r.errf("BASHPP-EREADONLY-MUTATION: cannot assign to readonly value %q\n", binding.object.owner)
	r.exit = exitStatus{code: 2}
}

// bashPPBuiltinAssign stores the single value a predeclared value builtin
// produces into an already declared target, keeping the result cell whole so
// that `s = append(s, 0)` carries the slice's payload, metadata and identity
// rather than a scalar spelling of it.
func (r *Runner) bashPPBuiltinAssign(assign *syntax.BashPPAssign) {
	target := bashPPWordSource(assign.Target)
	if r.bashPPGoSource && target == "_" && assign.Call != nil {
		name := bashPPPredeclaredCall(assign.Call)
		if !bashPPValueBuiltin(name) {
			r.bashPPBuiltinError("TYPE", "assignment call %s is not a supported value builtin", name)
			return
		}
		result, produced := r.bashPPRunValueBuiltin(name, assign.Call)
		if !produced || result == nil {
			if r.exit.code == 0 {
				r.bashPPBuiltinError("ARITY", "%s produces no value", name)
			}
		}
		return
	}
	cell := r.bashPPScope.lookup(target)
	if cell == nil || !syntax.BashPPValidIdent(target) {
		r.bashPPBuiltinError("TYPE", "assignment target %q is not declared", target)
		return
	}
	if guard := cell.view(); guard.constant || guard.vr.ReadOnly {
		r.errf("BASHPP-EREADONLY-MUTATION: cannot assign to readonly value %q\n", target)
		r.exit = exitStatus{code: 2}
		return
	}
	name := bashPPPredeclaredCall(assign.Call)
	if !bashPPValueBuiltin(name) {
		r.bashPPBuiltinError("TYPE", "assignment call %s is not a supported value builtin", name)
		return
	}
	result, produced := r.bashPPRunValueBuiltin(name, assign.Call)
	if !produced || result == nil {
		if r.exit.code == 0 {
			r.bashPPBuiltinError("ARITY", "%s produces no value", name)
		}
		return
	}
	owner := cell.view().object
	cell.publish(result)
	// The target keeps naming its own storage: append that reused the backing
	// array returns the source identity, and one that reallocated returns a
	// fresh one, but either way this variable is what owns it here.
	if stored := cell.view(); stored.object != nil && stored.object.owner == "" && owner != nil {
		stored.object.owner = owner.owner
	}
}

func (r *Runner) bashPPTupleAssignCall(ctx context.Context, assign *syntax.BashPPAssign) {
	if r.goSourceUnsafeDiscardAssign(assign) {
		return
	}
	if r.bashPPGoSource && r.bashPPBridgeHandles(assign.Call) {
		r.goSourceNativeAssignCall(ctx, assign)
		return
	}
	if r.bashPPGoSource && len(assign.Names) == 1 {
		if cell, handled, err := r.goSourceBuiltinResult(assign.Call); handled {
			if err != nil {
				if !errors.Is(err, errBashPPScalarInterrupted) {
					r.errf("%v\n", err)
					r.exit = exitStatus{code: 2}
				}
				return
			}
			r.bashPPCommitTupleAssign(assign, []*bashPPCell{cell})
			return
		}
	}
	// A local method selector is already a declared callable. Resolve it below
	// before asking the computed-callable path, whose native probe evaluates a
	// receiver expression. In particular, recover().(runtime.Error).Error()
	// must not evaluate recover once to inspect the callee and again to call it.
	localMethod := false
	if selector, ok := assign.Call.CalleeExpr.(*syntax.BashPPSelectorExpr); ok {
		localMethod = selector.MethodValue && !r.bashPPNativeExpr(selector.X)
	}
	if r.bashPPGoSource && !localMethod && (assign.Call.CalleeExpr != nil || len(assign.Call.Fun) >= 2) {
		if cells, handled := r.goSourceResultFuncValueCallCells(ctx, assign.Call); handled {
			if len(assign.Names) != len(cells) {
				r.errf("%sBASHPP-EASSIGN-ARITY: %d variable(s) but %d value(s)\n",
					r.bashErrPrefix(assign.Eq), len(assign.Names), len(cells))
				r.exit = exitStatus{code: 2}
				return
			}
			r.bashPPCommitTupleAssign(assign, cells)
			return
		}
	}
	fn, ok := r.bashPPLookupFunc(assign.Call)
	if !ok {
		// Resolving the callee can itself run code: `v = f().M()` calls f to
		// obtain the receiver. When that evaluation panicked, exited or
		// stopped on a fatal error, the lookup reports no callee because the
		// expression was interrupted, not because the callee is undeclared.
		// The recorded outcome is the result; the fallbacks below must neither
		// evaluate the receiver a second time nor replace that outcome with a
		// diagnostic about the call's shape.
		if r.bashPPPanicHalts() || r.exit.exiting || r.exit.fatalExit || r.exit.err != nil {
			return
		}
		if cells, handled := r.goSourceResultFuncValueCallCells(ctx, assign.Call); handled {
			if len(assign.Names) != len(cells) {
				r.errf("%sBASHPP-EASSIGN-ARITY: %d variable(s) but %d value(s)\n",
					r.bashErrPrefix(assign.Eq), len(assign.Names), len(cells))
				r.exit = exitStatus{code: 2}
				return
			}
			r.bashPPCommitTupleAssign(assign, cells)
			return
		}
		// `s = append(s, 0)`: the Go front end records the single target in
		// Names too, so a one-target assignment whose RHS is a predeclared
		// value builtin arrives here rather than at the single-target path. It
		// produces one value, not a tuple, and a declared function of the same
		// name has already been preferred by the lookup above.
		if len(assign.Names) == 1 && bashPPValueBuiltin(bashPPPredeclaredCall(assign.Call)) {
			r.bashPPBuiltinAssign(assign)
			return
		}
		// `v = recover()`: the recovered interface value, or the nil interface,
		// typed as the target's own static interface type (any / interface{} /
		// a named interface) so the assignment type check sees the variable's
		// type, not the spelling `any`.
		if r.goSourceRecoverAssign(assign) {
			return
		}
		if cell, handled := r.bashPPReturnRecover(assign.Call); handled && len(assign.Names) == 1 {
			r.bashPPCommitTupleAssign(assign, []*bashPPCell{cell})
			return
		}
		r.errf("%sBASHPP-EASSIGN-CALL: tuple assignment requires a declared result-bearing function\n", r.bashErrPrefix(assign.Call.Pos()))
		r.exit = exitStatus{code: 2}
		return
	}
	args, ok := r.bashPPCallValues(assign.Call, fn)
	if !ok {
		return
	}
	failureMark := r.bashPPShortFailureSeq
	var results []string
	if fn.foreign != nil && fn.foreign.receiver != nil && len(assign.Names) == 2 {
		results = r.bashPPInvokeForeignErr(ctx, fn.foreign, args)
	} else {
		results = r.bashPPInvoke(ctx, fn, args)
	}
	resultCells := r.bashPPResultCells
	if r.bashPPPanicking() || r.exit.exiting || r.exit.fatalExit || r.exit.err != nil || r.bashPPShortFailureSeq != failureMark {
		return
	}
	if len(assign.Names) != len(results) {
		r.errf("%sBASHPP-EASSIGN-ARITY: %d variable(s) but %d value(s)\n",
			r.bashErrPrefix(assign.Eq), len(assign.Names), len(results))
		r.exit = exitStatus{code: 2}
		return
	}
	candidates := make([]*bashPPCell, len(results))
	for i, result := range results {
		if i < len(resultCells) && resultCells[i] != nil {
			candidates[i] = bashPPCopyAssignmentCell(resultCells[i])
		} else {
			candidates[i] = &bashPPCell{vr: expand.Variable{Set: true, Kind: expand.String, Str: result}}
		}
	}
	r.bashPPCommitTupleAssign(assign, candidates)
}

func (r *Runner) bashPPTupleAssign(assign *syntax.BashPPAssign) {
	if r.goSourceReceiveAssign(assign) || r.goSourceCommaOkAssign(assign) {
		return
	}
	if len(assign.Values) == 0 || len(assign.ValueExprs) != len(assign.Values) {
		pos := assign.Eq
		if len(assign.Values) > 0 && assign.Values[0] != nil {
			pos = assign.Values[0].Pos()
		}
		r.errf("%sBASHPP-EASSIGN-FORM: tuple RHS is not a supported comma-delimited scalar expression list\n", r.bashErrPrefix(pos))
		r.exit = exitStatus{code: 2}
		return
	}
	if len(assign.Names) != len(assign.Values) {
		r.errf("%sBASHPP-EASSIGN-ARITY: %d variable(s) but %d value(s)\n",
			r.bashErrPrefix(assign.Eq), len(assign.Names), len(assign.Values))
		r.exit = exitStatus{code: 2}
		return
	}
	candidates := make([]*bashPPCell, len(assign.Values))
	for i, expr := range assign.ValueExprs {
		if expr == nil {
			r.errf("%sBASHPP-EASSIGN-FORM: tuple RHS %d is not a supported scalar expression\n",
				r.bashErrPrefix(assign.Values[i].Pos()), i+1)
			r.exit = exitStatus{code: 2}
			return
		}
		// Classify a parenthesized RHS by its operand so pointer and structured
		// values reach their existing single-evaluation paths. Keep the source
		// word unchanged for diagnostics and structured argument handling.
		expr = bashPPUnparenExpr(expr)
		// `f = i.(float64)` is an assertion, not a scalar expression, and its
		// one-result form panics rather than yielding a value when it fails.
		if cell, handled, err := r.bashPPAssertCandidate(expr); handled {
			if errors.Is(err, errBashPPScalarInterrupted) {
				return
			}
			if err != nil {
				r.errf("%s%v\n", r.bashErrPrefix(expr.Pos()), err)
				r.exit = exitStatus{code: 2}
				r.exit.fatal(ExitStatus(2))
				return
			}
			if r.exit.code != 0 {
				return
			}
			candidates[i] = cell
			continue
		}
		// `f = v.M`: a method value is the bound closure it denotes, exactly
		// as `f := v.M` binds it.
		if cell, handled, err := r.goSourceMethodValueCandidate(expr); handled {
			if err != nil {
				if !errors.Is(err, errBashPPScalarInterrupted) && !r.bashPPPanicking() {
					r.exit.fatal(err)
				}
				return
			}
			candidates[i] = cell
			continue
		}
		// An interface-typed target owns the dynamic type of what it is
		// given, so the value is built against the target's declared
		// interface rather than read as a plain scalar.
		if name := assign.Names[i]; name.Value != "_" {
			cell, handled, err := r.bashPPInterfaceAssignCandidate(r.bashPPScope.lookup(name.Value), expr)
			if err != nil {
				r.errf("%s%v\n", r.bashErrPrefix(expr.Pos()), err)
				r.exit = exitStatus{code: 2}
				return
			}
			if handled {
				candidates[i] = cell
				continue
			}
		}
		// A declared function has no variable cell; retain its callable
		// value just as declarations and call arguments already do.
		if cell, handled, err := r.goSourceCallableCell(expr); handled {
			if err != nil {
				if !errors.Is(err, errBashPPScalarInterrupted) {
					r.exit.fatal(err)
				}
				return
			}
			if cell.declType == nil {
				cell.declType = r.bashPPFuncValueType(cell)
			}
			candidates[i] = cell
			continue
		}
		if r.bashPPGoSource && r.bashPPNativeExpr(expr) {
			value, err := r.bashPPBridgeExpr(expr)
			if err != nil {
				r.exit.fatal(err)
				return
			}
			candidates[i] = goSourceNativeValueCell(value)
			continue
		}
		if candidate := r.goSourceNativeNilCandidate(r.bashPPScope.lookup(assign.Names[i].Value), expr); candidate != nil {
			candidates[i] = candidate
			continue
		}
		if candidate, handled, err := r.goSourceNilAssignCandidate(r.bashPPScope.lookup(assign.Names[i].Value), expr); handled {
			if err != nil {
				r.errf("%s%v\n", r.bashErrPrefix(expr.Pos()), err)
				r.exit = exitStatus{code: 2}
				return
			}
			candidates[i] = candidate
			continue
		}
		// `p, p.x.y = nil, 7` is split by the front end into temporaries
		// bound before the targets; the nil temporary carries no type of its
		// own, so it is typed here as the literal would be — the nil of the
		// target's own type.
		if candidate, handled, err := r.goSourceUntypedNilTemporaryCandidate(r.bashPPScope.lookup(assign.Names[i].Value), expr); handled {
			if err != nil {
				r.errf("%s%v\n", r.bashErrPrefix(expr.Pos()), err)
				r.exit = exitStatus{code: 2}
				return
			}
			candidates[i] = candidate
			continue
		}
		// A call used as one member of a parallel assignment still produces a
		// Go value, not merely its scalar spelling. In particular, `p, q =
		// makep(1), makep(2)` must retain each returned pointer's storage
		// identity. Evaluate the call once through the common value-cell path;
		// the candidate snapshot below is committed only after every RHS has
		// succeeded, preserving parallel-assignment ordering and atomicity.
		if _, call := expr.(*syntax.BashPPCall); call && r.bashPPGoSource {
			candidate, err := r.goSourceValueCell(expr)
			if err != nil {
				if !errors.Is(err, errBashPPScalarInterrupted) {
					r.errf("%s%v\n", r.bashErrPrefix(expr.Pos()), err)
					r.exit = exitStatus{code: 2}
				}
				return
			}
			candidates[i] = bashPPCopyAssignmentCell(candidate)
			continue
		}
		// `flag = true`: the predeclared booleans are identifiers with no
		// cell; unless a variable shadows them they are the constants the
		// scalar evaluator below knows.
		if ident, ok := expr.(*syntax.BashPPIdent); ok && !(bashPPBoolIdent(ident.Name.Value) && r.bashPPScope.lookup(ident.Name.Value) == nil) {
			source := r.bashPPScope.lookup(ident.Name.Value)
			if source == nil {
				r.errf("%sBASHPP-EASSIGN-UNDECLARED: RHS %s is not declared\n", r.bashErrPrefix(ident.Pos()), ident.Name.Value)
				r.exit = exitStatus{code: 2}
				return
			}
			candidates[i] = bashPPCopyAssignmentCell(source)
			continue
		}
		// `s = s[:0]`, `row = board[i]`: a RHS that reads structured storage has
		// no scalar spelling, so it is assigned as the value it is. A scalar
		// read returns no cell and falls through to the scalar evaluator, which
		// keeps owning that diagnostic.
		if cell, err := r.bashPPStructuredArgCell(assign.Values[i], expr); err != nil && r.bashPPGoSource {
			if !errors.Is(err, errBashPPScalarInterrupted) {
				r.errf("%s%v\n", r.bashErrPrefix(expr.Pos()), err)
				r.exit = exitStatus{code: 2}
			}
			return
		} else if err == nil && cell != nil {
			candidates[i] = cell
			continue
		}
		value, err := r.bashPPEvalScalarExpr(expr)
		if errors.Is(err, errBashPPScalarInterrupted) {
			return
		}
		if err != nil {
			if !errors.Is(err, errBashPPScalarInterrupted) {
				r.errf("%s%v\n", r.bashErrPrefix(expr.Pos()), err)
				r.exit = exitStatus{code: 2}
			}
			return
		}
		cell := &bashPPCell{vr: expand.Variable{Set: true, Kind: expand.String, Str: bashPPScalarString(value.value)}, scalarKind: value.value.Kind()}
		if value.typ != "" {
			cell.declType = &syntax.BashPPNamedType{Name: &syntax.Lit{Value: value.typ}}
			cell.typeName = value.typ
		}
		candidates[i] = cell
	}
	r.bashPPCommitTupleAssign(assign, candidates)
}

// goSourceUntypedNilTemporaryCandidate types an untyped nil held by a
// front-end temporary as the target's own nil. The tuple split binds
// `tuple_N := nil` before assigning `p = tuple_N`; the temporary's cell is
// the bare nil interface goSourceNilValueCell builds for the literal, and
// assigning it to a pointer, map, slice, func, chan or interface target must
// produce the value `p = nil` produces. The cell, not the identifier's
// spelling, establishes untyped-nil provenance; typed nil is not this path's.
func (r *Runner) goSourceUntypedNilTemporaryCandidate(target *bashPPCell, expr syntax.BashPPExpr) (*bashPPCell, bool, error) {
	if !r.bashPPGoSource || target == nil {
		return nil, false, nil
	}
	ident, ok := expr.(*syntax.BashPPIdent)
	if !ok || ident.Name == nil {
		return nil, false, nil
	}
	source := r.bashPPScope.lookup(ident.Name.Value)
	if source == nil || !goSourceUntypedNilCell(source) {
		return nil, false, nil
	}
	typ := target.declType
	if typ == nil {
		if meta := bashPPCellMeta(target); meta != nil && meta.typ != nil {
			typ = meta.typ
		} else {
			typ = bashPPInferredCellType(target)
		}
	}
	if typ == nil || !r.goSourceNilableType(typ) {
		return nil, false, nil
	}
	typed, err := r.goSourceExpectedCell(&bashPPCell{vr: expand.Variable{Set: true, Kind: expand.String}, interfaceValue: &bashPPInterfaceValue{nilIface: true}}, typ)
	if err != nil {
		return nil, true, err
	}
	return typed, true, nil
}

// goSourceUntypedNilCell reports whether cell holds the untyped nil literal
// itself: a nil interface with no declared or inferred type at all.
func goSourceUntypedNilCell(cell *bashPPCell) bool {
	// Eight fields of one value have to be zero together, so a shared cell is
	// tested through a snapshot; see bashpp_cell_share.go.
	cell = cell.view()
	return cell != nil && cell.interfaceValue != nil && cell.interfaceValue.nilIface &&
		cell.declType == nil && cell.typeName == "" && cell.valueMeta == nil &&
		cell.object == nil && !cell.pointer && cell.vr.Kind == expand.String && cell.vr.Str == ""
}

// bashPPCopyAssignmentCell snapshots the complete source cell while retaining
// Go's value/reference distinction: arrays and structs copy their payload,
// whereas slices, maps, channels, pointers, interfaces, and closures retain
// their reference identity and authoritative runtime metadata.
func bashPPCopyAssignmentCell(source *bashPPCell) *bashPPCell {
	if source == nil {
		return nil
	}
	// Read the source once: the carrier test and the payload it copies must
	// describe the same store, and the copy is private so it starts unaliased.
	// See bashpp_cell_share.go.
	snapshot := source.view()
	copyCell := *snapshot
	if snapshot.vr.Kind == expand.Object && snapshot.vr.Obj != nil && snapshot.object != nil && bashPPValueMeta(bashPPCellMeta(snapshot)) {
		value, meta := bashPPCopyArrayValue(snapshot.vr.Obj, bashPPCellMeta(snapshot))
		// The source already crossed the object boundary with authenticated
		// array/struct metadata. Preserve that carrier while replacing its
		// payload with the value copy: re-entering expand.NewObject would reject
		// legal Go values such as an array containing Inf as invalid JSON.
		copyCell.vr.Obj = value
		copyCell.valueMeta = meta
	}
	return &copyCell
}

func (r *Runner) bashPPCommitTupleAssign(assign *syntax.BashPPAssign, candidates []*bashPPCell) {
	targets := make([]*bashPPCell, len(assign.Names))
	for i, name := range assign.Names {
		if name.Value == "_" {
			continue
		}
		target := r.bashPPScope.lookup(name.Value)
		if target == nil {
			r.errf("%sBASHPP-EASSIGN-UNDECLARED: assignment target %s is not declared\n", r.bashErrPrefix(name.Pos()), name.Value)
			r.exit = exitStatus{code: 2}
			return
		}
		if view := target.view(); view.constant || view.vr.ReadOnly {
			r.errf("%sBASHPP-EASSIGN-CONST: cannot assign to %s\n", r.bashErrPrefix(name.Pos()), name.Value)
			r.exit = exitStatus{code: 2}
			return
		}
		r.bashPPPrepareInterfaceAssignment(target, candidates[i])
		if r.exit.code != 0 {
			return
		}
		if err := r.bashPPValidateReusedShortValue(target, candidates[i]); err != nil {
			// A forwarded program signal can arrive after a native call has
			// produced its results but while a reused target is being checked by
			// the native helper. The interruption is private control flow, not an
			// assignment diagnostic; the shared signal path owns status 128+sig.
			if errors.Is(err, errBashPPScalarInterrupted) {
				return
			}
			r.errf("%s%v\n", r.bashErrPrefix(name.Pos()), err)
			r.exit = exitStatus{code: 2}
			return
		}
		targets[i] = target
	}
	for i, target := range targets {
		if target == nil {
			continue
		}
		// The declaration's own attributes survive the assignment. Merge them
		// into the new value FIRST and store the result once: patching them
		// back after the store would republish the binding twice and let a
		// reader catch it wearing the candidate's declared type.
		view := target.view()
		merged := *candidates[i].view()
		merged.declType, merged.typeName = view.declType, view.typeName
		merged.constant = view.constant
		merged.vr.ReadOnly, merged.vr.Exported = view.vr.ReadOnly, view.vr.Exported
		target.publish(&merged)
	}
	r.exit.clear()
}

func (r *Runner) bashPPResolveWord(w *syntax.Word) (string, bool) {
	if !r.objectsEnabled() || r.bashPPScope == nil {
		return "", false
	}
	root, parts, ok := bashPPParsePathText(bashPPWordSource(w))
	if !ok || len(parts) == 0 {
		return "", false
	}
	cell := r.bashPPScope.lookup(root).view()
	if cell == nil || cell.vr.Kind != expand.Object {
		return "", false
	}
	value := cell.vr.Obj
	meta := bashPPCellMeta(cell)
	for _, part := range parts {
		if part.field != "" {
			if meta == nil {
				mapping, ok := value.(map[string]any)
				if !ok {
					return "", false
				}
				value, _ = bashPPStorageGet(mapping, part.field)
				continue
			}
			sel := r.bashPPResolveField(meta.typ, part.field)
			if sel.ambiguous || len(sel.edges) == 0 {
				return "", false
			}
			var err error
			value, meta, err = bashPPReadSelection(value, meta, sel.edges)
			if err != nil {
				return "", false
			}
			continue
		}
		switch current := value.(type) {
		case map[string]any:
			key, _ := part.index.(string)
			value, _ = bashPPStorageGet(current, key)
			if meta != nil {
				meta = bashPPLayoutGet(meta.mapping, key)
			}
		case []any:
			i, ok := part.index.(int)
			if !ok || i < 0 || i >= len(current) {
				return "", false
			}
			value = current[i]
			if meta != nil {
				meta = meta.sequence[i]
			}
		default:
			return "", false
		}
	}
	return fmt.Sprint(value), true
}

func bashPPParsePathText(text string) (string, []bashPPPathPart, bool) {
	i := 0
	for i < len(text) && (text[i] == '_' || text[i] >= 'a' && text[i] <= 'z' || text[i] >= 'A' && text[i] <= 'Z' || i > 0 && text[i] >= '0' && text[i] <= '9') {
		i++
	}
	root := text[:i]
	if !syntax.BashPPValidIdent(root) {
		return "", nil, false
	}
	var parts []bashPPPathPart
	for i < len(text) {
		switch text[i] {
		case '.':
			start := i
			i++
			fieldStart := i
			for i < len(text) && (text[i] == '_' || text[i] >= 'a' && text[i] <= 'z' || text[i] >= 'A' && text[i] <= 'Z' || i > fieldStart && text[i] >= '0' && text[i] <= '9') {
				i++
			}
			field := text[fieldStart:i]
			if !syntax.BashPPValidIdent(field) {
				return "", nil, false
			}
			parts = append(parts, bashPPPathPart{field: field, text: text[start:i]})
		case '[':
			start := i
			close := strings.IndexByte(text[i:], ']')
			if close < 0 {
				return "", nil, false
			}
			close += i
			raw := text[i+1 : close]
			var index any
			if quoted, err := strconv.Unquote(raw); err == nil {
				index = quoted
			} else if number, err := strconv.Atoi(raw); err == nil {
				index = number
			} else {
				return "", nil, false
			}
			i = close + 1
			parts = append(parts, bashPPPathPart{index: index, text: text[start:i]})
		default:
			return "", nil, false
		}
	}
	return root, parts, len(parts) > 0
}

func (r *Runner) bashPPShortDeclImported(ctx context.Context, d *syntax.BashPPShortDecl) bool {
	if d.Call == nil || len(d.Call.Fun) < 2 {
		return false
	}
	if _, imported := r.bashPPImports[d.Call.Fun[0].Value]; !imported {
		return false
	}
	evaluator, ok := r.bashPPTools.eval.(bashPPValuesEvaluator)
	if !ok {
		r.errf("bash++: selected evaluator cannot return object values\n")
		r.exit = exitStatus{code: 2}
		return true
	}
	// Mixed-mode imported calls run in a dependency session too. Besides
	// avoiding source-level references to interpreter locals, this preserves
	// the concrete Go type of structured results for a following selector.
	if r.bashPPTools.bridge == nil {
		r.bashPPTools.bridge = &bashPPNativeSession{}
	}
	req, err := r.bashPPEvalRequest()
	if err == nil {
		req.Results = len(d.Lhs)
		req.Selector = make([]string, len(d.Call.Fun))
		for i, part := range d.Call.Fun {
			req.Selector[i] = part.Value
		}
		req.Args = make([]string, len(d.Call.Args))
		for i, arg := range d.Call.Args {
			req.Args[i] = r.bashPPMixedImportArg(arg)
		}
		var values []any
		values, err = evaluator.Values(ctx, req)
		if err == nil {
			if len(values) != len(d.Lhs) {
				err = fmt.Errorf("assignment mismatch: %d variable(s) but %d value(s)", len(d.Lhs), len(values))
			} else {
				for i, lhs := range d.Lhs {
					if lhs.Value == "_" {
						continue
					}
					if _, native := values[i].(bashPPBridgeValue); native {
						continue
					}
					if validateErr := expand.ValidObject(values[i]); validateErr != nil {
						err = validateErr
						break
					}
				}
			}
			if err == nil {
				for i, lhs := range d.Lhs {
					if lhs.Value == "_" {
						continue
					}
					if native, ok := values[i].(bashPPBridgeValue); ok {
						r.bashPPBindNativeValue(lhs.Value, native)
						continue
					}
					r.bashPPDeclareName(lhs.Value, expand.NewObject(values[i]))
					r.bashPPScope.lookup(lhs.Value).object = &bashPPObjectIdentity{owner: lhs.Value}
				}
				return true
			}
		}
	}
	if err != nil {
		r.errf("%v\n", err)
		r.exit = exitStatus{code: 2}
	}
	return true
}

// bashPPMixedImportArg turns a bare interpreter scalar into a Go literal for
// the dependency helper. Other expressions retain their source spelling, so
// unsupported or undefined operands still receive Go's ordinary diagnostic.
func (r *Runner) bashPPMixedImportArg(arg *syntax.Word) string {
	name := bashPPWordSource(arg)
	if !syntax.BashPPValidIdent(name) || r.bashPPScope == nil {
		return name
	}
	cell := r.bashPPScope.lookup(name).view()
	if cell == nil || cell.vr.Kind != expand.String || cell.interfaceValue != nil || cell.pointer {
		return name
	}
	if cell.scalarKind == constant.String {
		return strconv.Quote(cell.vr.Str)
	}
	return cell.vr.Str
}
