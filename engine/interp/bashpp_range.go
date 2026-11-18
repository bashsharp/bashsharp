// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"context"
	"errors"
	"fmt"
	"go/constant"
	"math"
	"math/big"
	"sort"
	"strconv"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// goSourceRangeState mirrors the states Go's own rangefunc rewrite tracks per
// range statement (abi.RF_READY/RF_PANIC/RF_DONE/RF_EXHAUSTED): exactly one is
// current at a time, and a yield call made while it is anything but READY is a
// misuse whose message depends on which state it finds — see
// [Runner.goSourceInvokeRangeYield].
type goSourceRangeState uint8

const (
	// goSourceRangeReady: the body has not exited yet and is not running.
	goSourceRangeReady goSourceRangeState = iota
	// goSourceRangePanic: the body is either currently running, or the yield
	// call that ran it panicked and the panic has not yet been resolved (either
	// still unwinding, or swallowed by the iterator without calling yield
	// again — see the post-call check in [Runner.goSourceRangeFunction]).
	goSourceRangePanic
	// goSourceRangeDone: the body has exited in a non-panic way (break,
	// continue past this loop, or return).
	goSourceRangeDone
	// goSourceRangeExhausted: the iterator call itself has returned, so the
	// whole range statement is finished.
	goSourceRangeExhausted
)

// goSourceRangeYield is the interpreter-owned callback supplied to an iterator.
// Its body is still the authored range body; this state only carries the
// per-loop misuse state, the continuation result, and an outer return across
// the iterator's call frame.
type goSourceRangeYield struct {
	rng       *syntax.BashPPRange
	state     goSourceRangeState
	returning bool
	ret       bashPPReturnState
	// deferBuf collects the defers executed directly inside the range body
	// across every yield. They belong to the enclosing function, not to the
	// yield callback or the iterator frame, so they are held here — off the live
	// stack the iterator would truncate — and spliced onto the enclosing frame
	// once the iterator returns; see [Runner.goSourceRangeFunction].
	deferBuf []bashPPDeferred
	// pendingBranch, pendingBranchDepth and pendingGotoLabel hold a labeled
	// break/continue/goto that escaped this loop (bashPPBranchEscapesEligible
	// found more levels still to unwind) but has not yet reached the range
	// statement it targets. Go's own generated code threads this purely
	// through closure-captured #next variables the iterator function never
	// sees; our interpreter instead runs the range body directly, at whatever
	// call-stack depth the iterator happens to be invoking yield from, so the
	// runner-global branch state would otherwise stay visible to that
	// iterator's OWN unrelated loops for the rest of its call — for example
	// rangefunc_test.go's BadOfSliceIndex keeps calling yield in its own for
	// loop after a labeled continue escapes the range body, and that for
	// loop's own control check must not mistake the escaping label for its
	// own. [Runner.goSourceInvokeRangeYield] hides the escape here and clears
	// the runner-global state for the rest of the iterator's call;
	// [Runner.goSourceRangeFunction] reinstates it once the iterator call
	// returns, so it becomes visible again to the range statement's own
	// enclosing function.
	pendingBranch      bashPPBranchKind
	pendingBranchDepth int
	pendingGotoLabel   string
}

func (r *Runner) goSourceIteratorYield(fn *bashPPFunc) (*syntax.BashPPFuncType, error) {
	if fn == nil || len(fn.results()) != 0 {
		return nil, fmt.Errorf("iterator callback must have no results")
	}
	params := bashppParams(fn.params())
	if len(params) != 1 || params[0].variadic {
		return nil, fmt.Errorf("iterator callback must accept one yield function")
	}
	yield, ok := r.bashPPUnderlyingType(params[0].typ).(*syntax.BashPPFuncType)
	if !ok || bashppResultCount(yield.Results) != 1 || len(bashppParams(yield.Results)) != 1 ||
		bashPPTypeText(r.bashPPUnderlyingType(bashppParams(yield.Results)[0].typ)) != "bool" {
		return nil, fmt.Errorf("iterator yield must return one bool")
	}
	yieldParams := bashppParams(yield.Params)
	if len(yieldParams) > 2 {
		return nil, fmt.Errorf("iterator yield accepts at most two parameters")
	}
	for _, param := range yieldParams {
		if param.variadic {
			return nil, fmt.Errorf("iterator callback yield parameters must not be variadic")
		}
		// A native range iterator crosses the dependency callback boundary. Admit
		// exactly the shapes supported by that boundary: scalars and aggregate
		// values with copy semantics, or imported values that remain behind an
		// authenticated dependency handle. An interpreted iterator calls its
		// synthetic yield closure inside this Runner, so its typed cells do not
		// need dependency transport and retain reference identity here.
		if fn.native != nil && !r.bashPPCallbackValueType(param.typ) && !r.bashPPCallbackNativeType(param.typ) {
			return nil, fmt.Errorf("iterator callback yield parameter requires shared-reference transport: %s", bashPPTypeText(param.typ))
		}
	}
	return yield, nil
}

func (r *Runner) bashPPCallbackScalarType(typ syntax.BashPPTypeExpr) bool {
	underlying := r.bashPPUnderlyingType(typ)
	named, ok := underlying.(*syntax.BashPPNamedType)
	if !ok || named.Name == nil {
		return false
	}
	name := named.Name.Value
	return bashPPIntegerType(name) || name == "bool" || name == "string" || name == "float32" || name == "float64"
}

func goSourceRangeFuncType(expr syntax.BashPPExpr) *syntax.BashPPFuncType {
	switch x := expr.(type) {
	case *syntax.BashPPParenExpr:
		return goSourceRangeFuncType(x.X)
	case *syntax.BashPPCall:
		return x.ResultFuncType
	case *syntax.BashPPFuncLit:
		return bashPPFuncLitType(x)
	case *syntax.BashPPSelectorExpr:
		return x.FuncType
	}
	return nil
}

// goSourceRangeFunction executes the Go 1.23 range-over-function protocol. It
// is GoSource-only: Classic/Bash++ retain their existing explicit rejection.
func (r *Runner) goSourceRangeFunction(ctx context.Context, rng *syntax.BashPPRange) bool {
	if !r.bashPPGoSource || rng == nil || rng.Expr == nil {
		return false
	}
	var fn *bashPPFunc
	if cell, handled, err := r.goSourceCallableCell(rng.Expr); handled {
		if err != nil {
			r.bashPPRangeError(rng, "BASHPP-ERANGE-FUNC: %v", err)
			return true
		}
		fn, _ = r.bashPPClosure(cell.vr.Str)
	} else if signature := goSourceRangeFuncType(rng.Expr); signature != nil {
		cell, err := r.goSourceValueCell(rng.Expr)
		if err != nil {
			r.bashPPRangeError(rng, "BASHPP-ERANGE-FUNC: %v", err)
			return true
		}
		if local, ok := r.bashPPClosure(cell.vr.Str); ok {
			fn = local
		} else if value, ok := cell.vr.Obj.(*bashPPBridgeValue); ok && value.Kind == "handle" {
			native := *value
			native.Callable = "range-iterator"
			fn = &bashPPFunc{native: &native, lit: &syntax.BashPPFuncLit{Params: signature.Params, Results: signature.Results}}
		}
	}
	if fn == nil {
		return false
	}
	yieldType, err := r.goSourceIteratorYield(fn)
	if err != nil {
		r.bashPPRangeError(rng, "BASHPP-ERANGE-FUNC: %v", err)
		return true
	}
	if len(rng.Names) > len(bashppParams(yieldType.Params)) {
		r.bashPPRangeError(rng, "BASHPP-ERANGE-ARITY: iterator yields %d value(s)", len(bashppParams(yieldType.Params)))
		return true
	}
	state := &goSourceRangeYield{rng: rng, state: goSourceRangeReady}
	yield := &bashPPFunc{
		rangeYield: state,
		lit:        &syntax.BashPPFuncLit{Params: yieldType.Params, Results: yieldType.Results},
		scope:      r.bashPPScope,
	}
	vr := r.bashPPStoreFunc(yield)
	r.bashPPCallCells = []*bashPPCell{{vr: vr, declType: yieldType}}
	r.bashPPInvoke(ctx, fn, []string{vr.Str})
	// A labeled break/continue/goto that escaped this range's own body was
	// hidden from the runner-global branch state for the rest of the
	// iterator's call, so its own unrelated loops could not mistake it for
	// theirs; see the [goSourceRangeYield] field comments. Reinstate it now
	// that the iterator call has returned, so the enclosing function's own
	// statement execution and loop control see it again.
	if state.pendingBranch != bashPPBranchNone {
		r.bashPPBranch, r.bashPPBranchDepth, r.bashPPGotoLabel = state.pendingBranch, state.pendingBranchDepth, state.pendingGotoLabel
	}
	// The iterator call above has now returned or is still unwinding an
	// unrecovered panic. Go's own generated exhausted-check never runs while a
	// panic is still propagating past this frame — control simply never
	// reaches it — so only decide the post-call state when nothing is still
	// unwinding through us.
	if !r.bashPPPanicking() {
		if state.state == goSourceRangePanic {
			// The last body call panicked, and the iterator's own code
			// recovered that panic (directly or through a helper) without
			// ever calling yield again to transition the state away from
			// PANIC. That is a distinct misuse from either an ordinary false
			// return or the whole loop already having exited, and Go reports
			// it with its own runtime error.
			r.bashPPRaise("runtime error: range function recovered a loop body panic and did not resume panicking")
		} else {
			// The iterator call above has now returned normally, so the whole
			// range statement is exhausted: any further call to the yield
			// closure — typically one the iterator squirreled away and
			// invokes later, after escaping this frame — is a distinct misuse
			// from the body having already returned false while the iterator
			// was still running, and Go reports it with a different runtime
			// error; see [Runner.goSourceInvokeRangeYield].
			state.state = goSourceRangeExhausted
		}
	}
	// The body's defers were collected off the live stack so the iterator's
	// return could not run or discard them. They belong to the enclosing
	// function, so splice them onto its region now, in registration order: onto
	// the parent range's buffer if this range is itself nested in a body,
	// otherwise onto the live stack the enclosing frame will unwind. This runs
	// whether the iterator finished normally, stopped early, or panicked.
	if len(state.deferBuf) > 0 {
		if r.bashPPRangeDefer != nil {
			*r.bashPPRangeDefer = append(*r.bashPPRangeDefer, state.deferBuf...)
		} else {
			r.bashPPDeferStack = append(r.bashPPDeferStack, state.deferBuf...)
		}
	}
	if state.returning {
		r.bashPPReturn = state.ret
		r.exit.returning = true
	}
	return true
}

func (r *Runner) goSourceInvokeRangeYield(ctx context.Context, fn *bashPPFunc, args []string, cells []*bashPPCell, interfaces []*bashPPInterfaceValue) []string {
	state := fn.rangeYield
	// Capture the state found on entry, then mark the body running before
	// checking it: Go's own generated check sets #state = RF_PANIC
	// unconditionally before testing the old value, so a misbehaving iterator
	// that swallows this call's panic (below) and calls yield again still
	// finds PANIC rather than the stale DONE/EXHAUSTED — that is what lets the
	// post-iterator-call check in [Runner.goSourceRangeFunction] recognize a
	// loop body panic that was never resumed, instead of it looking like an
	// ordinary repeated misuse.
	entry := state.state
	state.state = goSourceRangePanic
	switch entry {
	case goSourceRangeExhausted:
		r.bashPPRaise("runtime error: range function continued iteration after whole loop exit")
		return nil
	case goSourceRangeDone:
		r.bashPPRaise("runtime error: range function continued iteration after function for loop body returned false")
		return nil
	case goSourceRangePanic:
		r.bashPPRaise("runtime error: range function continued iteration after loop body panic")
		return nil
	}
	params := bashppParams(fn.params())
	if len(args) != len(params) {
		r.exit.fatal(fmt.Errorf("gosource: iterator yield argument count mismatch"))
		return nil
	}
	values := make([]any, len(args))
	metas := make([]*bashPPCollectionMeta, len(args))
	for i := range args {
		values[i] = args[i]
		if i >= len(cells) || cells[i] == nil {
			continue
		}
		// The value carrier and its separately recorded interface identity must
		// come from snapshots. Callback arguments can name captured cells shared
		// with another interpreted task, whose direct fields are not authoritative.
		snapshot := *cells[i].view()
		snapshot.guard = nil
		cell := &snapshot
		// Interface identity is carried beside the legacy argument strings.
		// Ordinary function parameters consume that side channel while entering
		// their frame; a synthetic range yield has no ordinary frame, so join it
		// back to the value cell here before binding the iteration variable.
		if i < len(interfaces) && interfaces[i] != nil && cell.interfaceValue == nil {
			cell.interfaceValue = interfaces[i]
		}
		var value any
		var meta *bashPPCollectionMeta
		var err error
		if cell.interfaceValue != nil {
			value = cell.vrValue()
			meta = &bashPPCollectionMeta{kind: "interface", typ: cell.declType, interfaceValue: cell.interfaceValue}
		} else {
			value, meta, err = r.bashPPReadCellValue(cell)
		}
		if err != nil {
			r.exit.fatal(err)
			return nil
		}
		// An imported non-scalar callback parameter is deliberately kept as
		// the dependency-owned handle produced by goSourceCallbackCell. Give
		// range binding the native metadata it needs instead of rendering the
		// handle as text. Interfaces already carry richer dynamic-type metadata
		// from bashPPReadCellValue and keep it unchanged.
		if meta == nil && cell.vr.Kind == expand.Object && r.bashPPCallbackNativeType(params[i].typ) {
			meta = &bashPPCollectionMeta{kind: "native", typ: params[i].typ}
		}
		values[i], metas[i] = value, meta
	}
	savedScope := r.bashPPScope
	r.bashPPScope = fn.scope
	var key, value any
	var keyMeta, valueMeta *bashPPCollectionMeta
	var keyType, valueType syntax.BashPPTypeExpr
	if len(values) > 0 {
		key, keyMeta, keyType = values[0], metas[0], params[0].typ
	}
	if len(values) > 1 {
		value, valueMeta, valueType = values[1], metas[1], params[1].typ
	}
	// A defer executed directly in the body binds to the enclosing function,
	// so divert it to the state's buffer for the length of the body. A frame
	// entered for an ordinary call the body makes clears the sink itself, so
	// only the body's own defers are captured here.
	savedSink := r.bashPPRangeDefer
	r.bashPPRangeDefer = &state.deferBuf
	more := r.bashPPRangeIterationKeyMeta(ctx, state.rng, key, keyMeta, keyType, value, valueMeta, valueType)
	r.bashPPRangeDefer = savedSink
	r.bashPPScope = savedScope
	// A labeled break/continue/goto that still needs to unwind past this range
	// statement (bashPPRangeControl found it escaping, so more is already
	// false) must not stay visible to the iterator's OWN loops for the rest
	// of its call — see the [goSourceRangeYield] field comments. Hide it here;
	// [Runner.goSourceRangeFunction] reinstates it once the iterator call
	// returns.
	if r.bashPPBranch != bashPPBranchNone {
		state.pendingBranch, state.pendingBranchDepth, state.pendingGotoLabel = r.bashPPBranch, r.bashPPBranchDepth, r.bashPPGotoLabel
		r.bashPPBranch, r.bashPPBranchDepth, r.bashPPGotoLabel = bashPPBranchNone, 0, ""
	}
	if r.bashPPPanicking() {
		// The body panicked with a real Go panic (as opposed to break,
		// continue, or return) and that panic is still unwinding. Leave the
		// state at PANIC — a misbehaving iterator that swallows this panic and
		// calls yield again must see the distinct "after loop body panic"
		// error, not be treated as though the body merely returned false —
		// and report no result: the panic already carries the control
		// transfer out of this call.
		return nil
	}
	if r.exit.returning {
		state.returning, state.ret = true, r.bashPPReturn
		r.bashPPReturn = bashPPReturnState{}
		r.exit.returning = false
		more = false
	}
	if more {
		state.state = goSourceRangeReady
	} else {
		state.state = goSourceRangeDone
	}
	text := strconv.FormatBool(more)
	cell := &bashPPCell{vr: expand.Variable{Set: true, Kind: expand.String, Str: text}, scalarKind: constant.Bool, typeName: "bool", declType: bashPPRangeNamedType("bool")}
	r.bashPPResultCells = []*bashPPCell{cell}
	return []string{text}
}

func (r *Runner) goSourceInvokeCollectYield(fn *bashPPFunc, args []string, cells []*bashPPCell) []string {
	if len(args) != 1 || len(cells) != 1 || cells[0] == nil {
		r.exit.fatal(fmt.Errorf("gosource: slices.Collect iterator yield argument count mismatch"))
		return nil
	}
	value, err := r.bashPPBridgeCell(cells[0])
	if err != nil {
		r.exit.fatal(err)
		return nil
	}
	*fn.collectYield = append(*fn.collectYield, value)
	cell := &bashPPCell{vr: expand.Variable{Set: true, Kind: expand.String, Str: "true"}, scalarKind: constant.Bool, typeName: "bool", declType: bashPPRangeNamedType("bool")}
	r.bashPPResultCells = []*bashPPCell{cell}
	return []string{"true"}
}

// bashPPRangeScalar handles the two non-container range operands whose values
// live in the scalar namespace: strings and integers. It deliberately leaves
// channels and structured values to their existing paths. Function iterators
// are rejected explicitly: the current erased `func` type cannot encode Go's
// required func(func(...) bool) signature, so executing one would be an unsafe
// second semantics rather than Go-compatible range.
func (r *Runner) bashPPRangeScalar(ctx context.Context, rng *syntax.BashPPRange) bool {
	if rng.Expr == nil {
		return false
	}
	if root, ok := bashPPCollectionRoot(rng.Expr); ok && r.bashPPScope != nil {
		cell := r.bashPPScope.lookup(root).view()
		if cell != nil {
			if cell.channel != nil {
				return false
			}
			if _, closure := r.bashPPClosure(cell.vr.String()); closure {
				r.bashPPRangeError(rng, "BASHPP-ERANGE-FUNC: range over function requires a typed func(func(...) bool) iterator")
				return true
			}
		}
		if cell == nil && r.bashPPFuncs[root] != nil {
			r.bashPPRangeError(rng, "BASHPP-ERANGE-FUNC: range over function requires a typed func(func(...) bool) iterator")
			return true
		}
	}

	// A dependency-owned sequence is read through its handle; see
	// bashPPNativeRange in bashpp_native_access.go.
	if r.bashPPNativeRange(ctx, rng) {
		return true
	}

	value, err := r.bashPPEvalScalarExpr(rng.Expr)
	if err != nil {
		if !errors.Is(err, errBashPPScalarInterrupted) {
			r.bashPPRangeError(rng, "BASHPP-ERANGE-TYPE: %v", err)
		}
		return true
	}
	return r.bashPPRangeScalarValue(ctx, rng, value)
}

// bashPPRangeScalarValue iterates an already evaluated scalar range operand.
func (r *Runner) bashPPRangeScalarValue(ctx context.Context, rng *syntax.BashPPRange, value bashPPScalar) bool {
	switch value.value.Kind() {
	case constant.String:
		text := constant.StringVal(value.value)
		for offset, runeValue := range text {
			if !r.bashPPRangeIteration(ctx, rng, offset, bashPPRangeNamedType("int"), int64(runeValue), nil,
				&syntax.BashPPNamedType{Name: &syntax.Lit{Value: "rune"}}) {
				return true
			}
		}
		return true
	case constant.Int:
		if len(rng.Names) > 1 {
			r.bashPPRangeError(rng, "BASHPP-ERANGE-ARITY: integer range permits at most one iteration variable")
			return true
		}
		limit, ok := constant.Int64Val(value.value)
		if !ok {
			// A uint64 or uintptr bound above the int64 range is a legal
			// Go bound. The counter below is an int64, so the loop walks
			// the indexes it can represent; no program reaches the 2^63
			// iterations it would take to observe the remainder, and a
			// break or return leaves the loop as it does any other.
			if constant.Sign(value.value) <= 0 {
				r.bashPPRangeError(rng, "BASHPP-ERANGE-INTEGER: integer range bound is not representable as int64")
				return true
			}
			limit = math.MaxInt64
		}
		// An empty integer range with no bindings has no per-iteration
		// language work. Keep its counter in Go and visit the ordinary
		// command/cancellation path periodically rather than dispatching
		// an empty AST block for every increment. Traced execution retains
		// the original path and all nonempty bodies still run normally.
		if !r.opts[optXTrace] && !bashPPRangeBindsNames(rng) && rng.Body != nil && len(rng.Body.Stmts) == 0 {
			for i := int64(0); i < limit; i++ {
				if i&1023 == 0 {
					r.cmd(r.bashPPTaskContext(ctx), rng.Body)
					if !r.bashPPRangeControl() {
						return true
					}
				}
			}
			return true
		}
		iterationType := bashPPRangeNamedType("int")
		if value.typ != "" {
			iterationType = bashPPRangeNamedType(value.typ)
		}
		if ident, ok := rng.Expr.(*syntax.BashPPIdent); ok && r.bashPPScope != nil {
			if cell := r.bashPPScope.lookup(ident.Name.Value).view(); cell != nil && cell.declType != nil {
				iterationType = cell.declType
			}
		}
		for i := int64(0); i < limit; i++ {
			if !r.bashPPRangeIteration(ctx, rng, i, iterationType, nil, nil, nil) {
				return true
			}
		}
		return true
	default:
		r.bashPPRangeError(rng, "BASHPP-ERANGE-TYPE: cannot range over %s", value.value.Kind())
		return true
	}
}

func bashPPRangeNamedType(name string) syntax.BashPPTypeExpr {
	return &syntax.BashPPNamedType{Name: &syntax.Lit{Value: name}}
}

func (r *Runner) bashPPRangeError(rng *syntax.BashPPRange, format string, args ...any) {
	pos := rng.Range
	if rng.Expr != nil {
		pos = rng.Expr.Pos()
	}
	r.errf("%s%s\n", r.bashErrPrefix(pos), fmt.Sprintf(format, args...))
	r.exit = exitStatus{code: 2}
}

func (r *Runner) bashPPRangeCollection(ctx context.Context, rng *syntax.BashPPRange) bool {
	if rng.Expr == nil || r.bashPPScope == nil {
		return false
	}
	// `range f()`: a call's result is read once, as the cell the callee
	// returned. A collection result ranges as that collection; a scalar
	// result — a string, an integer — takes the scalar iteration without
	// being evaluated a second time.
	if call, isCall := rng.Expr.(*syntax.BashPPCall); isCall && r.bashPPGoSource && !r.bashPPBridgeHandles(call) {
		cell, err := r.goSourceValueCell(call)
		if err != nil {
			if !errors.Is(err, errBashPPScalarInterrupted) {
				r.bashPPRangeError(rng, "BASHPP-ERANGE-TYPE: %v", err)
			}
			return true
		}
		if cell.vr.Kind == expand.Object && bashPPCellMeta(cell) != nil {
			return r.bashPPRangeCollectionValue(ctx, rng, cell.vr.Obj, bashPPCellMeta(cell))
		}
		if cell.pointer {
			return r.goSourceRangePointerArray(ctx, rng, cell.pointerValue, bashPPPointerMeta(cell.declType))
		}
		if native, ok := r.goSourceNativeChannel(cell); ok {
			r.goSourceRangeNativeChannel(ctx, rng, native)
			return true
		}
		if cell.channel != nil {
			if cell.channelOwner != r.bashPPConcurrent {
				r.bashPPRangeError(rng, "Go channel belongs to another task group")
				return true
			}
			r.bashPPRangeChannel(ctx, rng, cell.channel)
			return true
		}
		if cell.vr.Kind == expand.Object {
			return false
		}
		return r.bashPPRangeScalarValue(ctx, rng, r.bashPPScalarFromCell(cell))
	}
	// `range []byte(s)`, `range []rune(f())`: a conversion that produces a
	// collection is ranged as the slice it builds. The operand is evaluated
	// once, inside the conversion; a scalar conversion is not claimed here
	// and keeps the scalar range path.
	if conv, isConv := rng.Expr.(*syntax.BashPPConvertExpr); isConv && r.bashPPGoSource {
		cell, handled, err := r.bashPPConvertCollectionCell(conv)
		if err != nil {
			if !errors.Is(err, errBashPPScalarInterrupted) {
				r.bashPPRangeError(rng, "BASHPP-ERANGE-TYPE: %v", err)
			}
			return true
		}
		if handled && cell.vr.Kind == expand.Object && bashPPCellMeta(cell) != nil {
			return r.bashPPRangeCollectionValue(ctx, rng, cell.vr.Obj, bashPPCellMeta(cell))
		}
	}
	root, ok := bashPPCollectionRoot(rng.Expr)
	if !ok {
		// A composite is a range value in its own right. It has no lexical root
		// cell, so materialize it exactly once below instead of passing it to the
		// scalar evaluator. The resulting metadata preserves array-copy and
		// slice/map reference semantics.
		// `range &arr` is a pointer to the array, read as a value the same
		// way; see goSourceRangePointerArray.
		operand := rng.Expr
		for {
			paren, ok := operand.(*syntax.BashPPParenExpr)
			if !ok {
				break
			}
			operand = paren.X
		}
		_, address := operand.(*syntax.BashPPAddressExpr)
		if _, composite := operand.(*syntax.BashPPCompositeLit); !composite && !(address && r.bashPPGoSource) {
			return false
		}
	}
	var cell *bashPPCell
	if ok {
		// One snapshot: the channel test and every operand test below decide
		// about the same store of a possibly shared cell.
		cell = r.bashPPScope.lookup(root).view()
		if cell == nil || cell.channel != nil {
			return false
		}
	}
	// A variadic parameter binds as an indexed variable rather than an
	// object — see [Runner.bashPPInvoke] — so it carries its collection meta
	// on the side instead of behind cell.object. A bare `range rest` is the
	// only shape that meta describes; a path rooted in one more scalars would
	// still need the Object machinery below, which a variadic parameter never
	// has.
	if _, isIdent := rng.Expr.(*syntax.BashPPIdent); isIdent && cell != nil && cell.vr.Kind == expand.Indexed && cell.valueMeta != nil {
		return r.bashPPRangeIndexed(ctx, rng, cell)
	}
	if cell != nil && cell.vr.Kind != expand.Object && !cell.pointer {
		return false
	}
	if cell != nil {
		if _, native := r.goSourceNativeChannel(cell); native {
			return false
		}
	}
	if r.goSourceRangeDerefArray(ctx, rng, cell) {
		return true
	}
	value, meta, err := r.bashPPReadExpr(rng.Expr)
	if err != nil {
		if !errors.Is(err, errBashPPScalarInterrupted) {
			r.bashPPRangeError(rng, "%v", err)
		}
		return true
	}
	// A path rooted in a collection can still select an ordinary scalar, for
	// example matrix[0][0] or cfg.Limit. Let the scalar range path evaluate
	// those values; metadata is only present for structured results.
	if meta == nil {
		return false
	}
	// A scalar read out of dependency-owned storage — `*trials` where trials is
	// the *int64 flag.Int64 returned — is still a scalar, but arrives with
	// metadata: the wire spells every integer alike, so the declared Go type
	// travels beside the value. That metadata describes no collection. Range
	// the value just read as the scalar it is, under its declared type, rather
	// than refusing it as a non-collection or evaluating the operand again.
	if bashPPScalarMetaKind(meta) {
		return r.bashPPRangeScalarValue(ctx, rng, r.bashPPRangeTypedScalar(value, meta))
	}
	// A "native" kind means the value is still a lazy dependency-owned handle
	// (see bashPPBridgeContents) rather than interpreter-owned storage this
	// path knows how to walk — for example a reassigned `lines = strings.
	// Split(...)` result kept lazy for identity. Fall through to the native
	// range path in bashPPRangeScalar, which reads each element back through
	// the handle instead of claiming the range and iterating zero elements.
	if meta.kind == "native" {
		return false
	}
	return r.bashPPRangeCollectionValue(ctx, rng, value, meta)
}

// bashPPRangeTypedScalar rebuilds the scalar a structured read projected into
// a Go value together with scalar metadata. An integer too wide for the
// projection's int (a uint64 above the int range) is carried as its decimal
// spelling; it is still an integer unless the declared type is a string.
func (r *Runner) bashPPRangeTypedScalar(value any, meta *bashPPCollectionMeta) bashPPScalar {
	scalar := bashPPScalar{value: bashPPScalarConstant(value)}
	if meta.typ != nil {
		scalar.typ = bashPPTypeText(meta.typ)
	}
	text, isText := value.(string)
	if !isText {
		return scalar
	}
	if named, ok := r.bashPPUnderlyingType(meta.typ).(*syntax.BashPPNamedType); ok && named.Name != nil && named.Name.Value == "string" {
		return scalar
	}
	if n, ok := new(big.Int).SetString(text, 10); ok && meta.typ != nil {
		scalar.value = constant.Make(n)
	}
	return scalar
}

// bashPPRangeCollectionValue iterates an already read collection operand.
func (r *Runner) bashPPRangeCollectionValue(ctx context.Context, rng *syntax.BashPPRange, value any, meta *bashPPCollectionMeta) bool {
	if r.goSourceRangePointerArray(ctx, rng, value, meta) {
		return true
	}
	if meta.kind == "struct" || meta.kind == "pointer" {
		r.bashPPRangeError(rng, "BASHPP-ERANGE-TYPE: cannot range over %s", bashPPTypeText(meta.typ))
		return true
	}
	collection, ok := r.bashPPUnderlyingType(meta.typ).(*syntax.BashPPCollectionType)
	if !ok {
		r.bashPPRangeError(rng, "BASHPP-ERANGE-TYPE: cannot range over %s", bashPPTypeText(meta.typ))
		return true
	}
	switch meta.kind {
	case "array", "inferred-array":
		// One key type serves the whole loop: it is only ever read.
		keyType := bashPPRangeNamedType("int")
		if !bashPPRangeBindsValue(rng) {
			// With at most one iteration variable Go ranges an array from 0
			// to len-1 and never indexes it, so there is no copy to observe:
			// the length is the type's, whatever the body assigns.
			length := len(value.([]any))
			first, finished := r.bashPPRangeReusedKey(ctx, rng, keyType, length)
			if finished {
				return true
			}
			for i := first; i < length; i++ {
				if !r.bashPPRangeIteration(ctx, rng, i, keyType, nil, nil, collection.Element) {
					return true
				}
			}
			break
		}
		value, meta = bashPPCopyArrayValue(value, meta)
		sequence := value.([]any)
		for i, elem := range sequence {
			if !r.bashPPRangeIteration(ctx, rng, i, keyType, elem, meta.sequence[i], collection.Element) {
				return true
			}
		}
	case "slice":
		sequence, _ := value.([]any)
		length := len(sequence)
		keyType := bashPPRangeNamedType("int")
		first, finished := r.bashPPRangeReusedKey(ctx, rng, keyType, length)
		if finished {
			return true
		}
		for i := first; i < length; i++ {
			if !r.bashPPRangeIteration(ctx, rng, i, keyType, sequence[i], meta.sequence[i], collection.Element) {
				return true
			}
		}
	case "map":
		mapping, _ := value.(map[string]any)
		if bashPPSprint165MapHasTypedKeys(meta) {
			for _, entry := range bashPPSprint165MapEntries(meta) {
				item, child, exists := bashPPSprint165MapEntryValue(mapping, meta, entry.storage)
				if !exists {
					continue
				}
				if !r.bashPPRangeIterationKeyMeta(ctx, rng, entry.key, entry.keyMeta, collection.Key, item, child, collection.Element) {
					return true
				}
			}
			break
		}
		keys := bashPPStorageKeys(mapping)
		sort.Strings(keys)
		for _, key := range keys {
			item, exists := bashPPStorageGet(mapping, key)
			if !exists {
				continue
			}
			if !r.bashPPRangeIteration(ctx, rng, key, collection.Key, item, bashPPLayoutGet(meta.mapping, key), collection.Element) {
				return true
			}
		}
	}
	return true
}

// bashPPRangeIndexed ranges the indexed binding a variadic parameter gets in
// [Runner.bashPPInvoke], using the collection meta attached there instead of
// the string list's length: element i's value and declared element type come
// from cell.valueMeta, exactly as they would for a slice held behind an
// Object cell.
func (r *Runner) bashPPRangeIndexed(ctx context.Context, rng *syntax.BashPPRange, cell *bashPPCell) bool {
	meta := cell.valueMeta
	collection, ok := r.bashPPUnderlyingType(meta.typ).(*syntax.BashPPCollectionType)
	if !ok {
		r.bashPPRangeError(rng, "BASHPP-ERANGE-TYPE: cannot range over %s", bashPPTypeText(meta.typ))
		return true
	}
	for i, elem := range cell.vr.List {
		var elemMeta *bashPPCollectionMeta
		if i < len(meta.sequence) {
			elemMeta = meta.sequence[i]
		}
		if !r.bashPPRangeIteration(ctx, rng, i, bashPPRangeNamedType("int"), elem, elemMeta, collection.Element) {
			return true
		}
	}
	return true
}

func (r *Runner) bashPPRangeIteration(ctx context.Context, rng *syntax.BashPPRange, key any, keyType syntax.BashPPTypeExpr, value any, valueMeta *bashPPCollectionMeta, valueType syntax.BashPPTypeExpr) bool {
	return r.bashPPRangeIterationKeyMeta(ctx, rng, key, nil, keyType, value, valueMeta, valueType)
}

// bashPPRangeIterationKeyMeta keeps the actual typed map-key cell when a map
// uses the comparable-key store. In particular, a struct key ranged from a
// generic map must not be flattened to its printable storage spelling before
// append or assignment consumes it.
func (r *Runner) bashPPRangeIterationKeyMeta(ctx context.Context, rng *syntax.BashPPRange, key any, keyMeta *bashPPCollectionMeta, keyType syntax.BashPPTypeExpr, value any, valueMeta *bashPPCollectionMeta, valueType syntax.BashPPTypeExpr) bool {
	// The iteration scope holds only the iteration variables; the body is a
	// block that pushes its own scope. A range binding no names (for range n,
	// or only blanks) therefore needs no per-iteration scope of its own.
	leave := bashPPNoScopeLeave
	if bashPPRangeBindsNames(rng) {
		leave = r.bashPPPushScope()
	}
	if len(rng.Names) >= 1 && rng.Names[0].Value != "_" {
		key, keyMeta = bashPPCopyArrayValue(key, keyMeta)
		r.bashPPDeclareRangeValue(rng.Names[0].Value, key, keyType, keyMeta)
	}
	if len(rng.Names) == 2 && rng.Names[1].Value != "_" {
		value, valueMeta = bashPPCopyArrayValue(value, valueMeta)
		r.bashPPDeclareRangeValue(rng.Names[1].Value, value, valueType, valueMeta)
	}
	r.cmd(r.bashPPTaskContext(ctx), rng.Body)
	leave()
	return r.bashPPRangeControl()
}

func bashPPNoScopeLeave() {}

// bashPPRangeBindsNames reports whether a range declares any non-blank
// iteration variable.
func bashPPRangeBindsNames(rng *syntax.BashPPRange) bool {
	for i, name := range rng.Names {
		if i < 2 && name.Value != "_" {
			return true
		}
	}
	return false
}

// bashPPRangeBindsValue reports whether a range declares a non-blank second
// iteration variable, the only one that reads an element.
func bashPPRangeBindsValue(rng *syntax.BashPPRange) bool {
	return len(rng.Names) == 2 && rng.Names[1].Value != "_"
}

// bashPPRangeControl reduces the runner state one executed loop body leaves to
// whether the range should continue. Every range shape shares it, so break,
// continue, return and abandonment behave identically across them.
func (r *Runner) bashPPRangeControl() bool {
	if r.exit.exiting || r.exit.returning || r.exit.fatalExit || r.loopControlPending() {
		return false
	}
	switch r.bashPPBranch {
	case bashPPBranchBreak:
		if r.bashPPBranchEscapesEligible() {
			return false
		}
		r.bashPPClearBranch()
		return false
	case bashPPBranchContinue:
		if r.bashPPBranchEscapesEligible() {
			return false
		}
		r.bashPPClearBranch()
	case bashPPBranchFallthrough:
		panic("validated fallthrough escaped to Bash++ range")
	case bashPPBranchGoto:
		return false
	}
	return true
}

func (r *Runner) bashPPDeclareRangeValue(name string, value any, typ syntax.BashPPTypeExpr, meta *bashPPCollectionMeta) {
	if meta != nil && (meta.kind == "pointer" || meta.kind == "channel" || r.bashPPGoSource && meta.interfaceValue != nil) {
		r.bashPPDeclareName(name, expand.Variable{Set: true, Kind: expand.String})
		cell := r.bashPPScope.lookup(name)
		cell.declType = typ
		bashPPStoreCellValue(cell, value, meta)
		return
	}
	if meta != nil {
		r.bashPPDeclareName(name, bashPPCollectionVariable(value))
		cell := r.bashPPScope.lookup(name)
		cell.object = &bashPPObjectIdentity{owner: name, collection: meta}
		cell.valueMeta = meta
		cell.declType = typ
		if named, ok := typ.(*syntax.BashPPNamedType); ok {
			cell.typeName = named.Name.Value
		}
		return
	}
	// An int key is by far the commonest iteration value; its spelling is the
	// one fmt would produce.
	text := ""
	if n, ok := value.(int); ok {
		text = strconv.Itoa(n)
	} else {
		text = fmt.Sprint(value)
	}
	r.bashPPDeclareName(name, expand.Variable{Set: true, Kind: expand.String, Str: text})
	cell := r.bashPPScope.lookup(name)
	cell.declType = typ
	if named, ok := typ.(*syntax.BashPPNamedType); ok {
		cell.typeName = named.Name.Value
	}
}
