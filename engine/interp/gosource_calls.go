package interp

import (
	"context"
	"errors"
	"fmt"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// bashPPGoSourceTupleCall evaluates an interpreted callable exactly once and
// retains every result cell for Go's sole-multi-value-argument rule.
func (r *Runner) bashPPGoSourceTupleCall(call *syntax.BashPPCall) ([]*bashPPCell, error) {
	fn, ok := r.bashPPLookupFunc(call)
	if !ok {
		if r.goSourceNilFuncCallee(call) {
			return nil, r.goSourceRuntimeFault(errBashPPNilDereference)
		}
		if r.goSourceCalleeFaulted() {
			return nil, errBashPPScalarInterrupted
		}
		return nil, fmt.Errorf("gosource: undefined interpreted callable")
	}
	return r.goSourceCallResultCells(call, fn)
}

func (r *Runner) goSourceCallResultCells(call *syntax.BashPPCall, fn *bashPPFunc) ([]*bashPPCell, error) {
	return r.goSourceCallResultCellsWithContext(r.ectx, call, fn)
}

func (r *Runner) goSourceResultFuncValueCallCells(ctx context.Context, call *syntax.BashPPCall) ([]*bashPPCell, bool) {
	if !r.bashPPGoSource || call == nil {
		return nil, false
	}
	fn, ok := r.goSourceFuncValueCallee(call)
	if !ok {
		return nil, false
	}
	cells, err := r.goSourceCallResultCellsWithContext(ctx, call, fn)
	if err != nil {
		if !errors.Is(err, errBashPPScalarInterrupted) && !r.bashPPPanicking() {
			r.exit.fatal(err)
		}
		return nil, true
	}
	return cells, true
}

func (r *Runner) goSourceCallResultCellsWithContext(ctx context.Context, call *syntax.BashPPCall, fn *bashPPFunc) ([]*bashPPCell, error) {
	var ok bool
	var args []string
	if call.ArgExprs != nil {
		var err error
		if args, ok, err = r.bashPPTypedCallArgs(call, fn); err != nil {
			return nil, err
		}
	} else {
		args, ok = r.bashPPCallValues(call, fn)
	}
	if !ok {
		return nil, errBashPPScalarInterrupted
	}
	previous := r.bashPPResultCells
	defer func() { r.bashPPResultCells = previous }()
	failure := r.bashPPShortFailureSeq
	values := r.bashPPInvoke(ctx, fn, args)
	if r.bashPPPanicHalts() || r.exit.exiting || r.exit.fatalExit || r.exit.err != nil || r.bashPPShortFailureSeq != failure || len(values) != len(r.bashPPResultCells) {
		return nil, errBashPPScalarInterrupted
	}
	results := make([]*bashPPCell, len(values))
	for i, c := range r.bashPPResultCells {
		results[i] = bashPPCopyAssignmentCell(c)
	}
	return results, nil
}

func (r *Runner) goSourceFuncValueCallee(call *syntax.BashPPCall) (*bashPPFunc, bool) {
	if fn, ok := r.goSourceComputedNativeFunc(call); ok {
		return fn, true
	}
	expr := call.CalleeExpr
	if expr == nil {
		expr = bashPPCallFunExpr(call)
	}
	if expr == nil {
		return nil, false
	}
	cell, err := r.goSourceValueCell(expr)
	if err != nil || cell == nil {
		return nil, false
	}
	fn, ok := r.bashPPClosure(cell.vr.Str)
	if !ok {
		return nil, false
	}
	if sig := bashPPComputedCalleeSignature(expr, cell); sig != nil {
		if bound, status := r.bashPPContextualFuncValue(fn, sig); status == bashPPBindOK {
			fn = bound
		}
	}
	return r.bashPPInstantiateFunc(call, fn)
}

func bashPPCallFunExpr(call *syntax.BashPPCall) syntax.BashPPExpr {
	if call == nil || len(call.Fun) == 0 {
		return nil
	}
	var expr syntax.BashPPExpr = &syntax.BashPPIdent{Name: call.Fun[0]}
	for _, part := range call.Fun[1:] {
		expr = &syntax.BashPPSelectorExpr{X: expr, Sel: part}
	}
	return expr
}

// bashPPGoSourceChanCall answers the channel builtins a Go region spells as an
// ordinary call. `close(ch)` has no shell spelling to recognize at parse time,
// so it arrives as a call node; it is the channel operation only where the
// session declared nothing of that name, exactly as a Go declaration shadows a
// predeclared identifier.
func (r *Runner) bashPPGoSourceChanCall(c *syntax.BashPPCall) bool {
	if !r.bashPPGoSource || len(c.Fun) != 1 || c.Fun[0].Value != "close" || len(c.Args) != 1 {
		return false
	}
	if r.bashPPFuncs["close"] != nil || (r.bashPPScope != nil && r.bashPPScope.lookup("close") != nil) {
		return false
	}
	if len(c.ArgExprs) == 1 && c.ArgExprs[0] != nil {
		r.goSourceCloseChannel(c.ArgExprs[0])
		return true
	}
	r.bashPPClose(&syntax.BashPPClose{Kw: c.Fun[0], Chan: c.Args[0], Lparen: c.Lparen, Rparen: c.Rparen})
	return true
}

// bashPPGoSourceEvaluatedCall returns call with every computed scalar argument
// replaced by the value it evaluates to. A Go region carries an argument as its
// original expression text, so `f(cap(c))` would otherwise bind the string
// "cap(c)". Only an argument the scalar runtime can answer is rewritten;
// anything else — an identifier, a channel, a slice expression — keeps the word
// the established binding already understands.
//
// The rewrite is also the evaluation order `go f(...)` requires: Go evaluates a
// launched call's arguments in the goroutine that launches it, not in the task.
func (r *Runner) bashPPGoSourceEvaluatedCall(call *syntax.BashPPCall) *syntax.BashPPCall {
	if !r.bashPPGoSource || call == nil || len(call.ArgExprs) != len(call.Args) {
		return call
	}
	out := call
	for i, expr := range call.ArgExprs {
		if !bashPPGoComputedArg(expr, call.Args[i]) {
			continue
		}
		replacement, ok := r.bashPPGoArgWord(call, i, expr)
		if !ok {
			continue
		}
		if out == call {
			copied := *call
			copied.Args = append([]*syntax.Word(nil), call.Args...)
			out = &copied
		}
		out.Args[i] = replacement
	}
	if out != call {
		// This clone carries values prepared in the launching goroutine.
		// Original expression trees must not execute again in the child.
		out.ArgExprs = nil
	}
	return out
}

// bashPPGoArgWord answers the word that carries an evaluated argument.
//
// A scalar becomes its own quoted value. A collection cannot be spelled as a
// word at all, so it is bound to a reserved name derived from the call site and
// the argument's position — a name no Go source can spell, and one that is
// reused rather than accumulated when the same site runs again — and the word
// becomes that name, which is the spelling the established argument binding
// already resolves to a cell.
func (r *Runner) bashPPGoArgWord(call *syntax.BashPPCall, i int, expr syntax.BashPPExpr) (*syntax.Word, bool) {
	word := call.Args[i]
	if value, err := r.bashPPEvalScalarExpr(expr); err == nil && value.value != nil && !value.hasNonFinite {
		return &syntax.Word{Parts: []syntax.WordPart{&syntax.SglQuoted{
			Left: word.Pos(), Right: word.End(), Value: bashPPScalarString(value.value),
		}}}, true
	}
	switch expr.(type) {
	case *syntax.BashPPIndexExpr, *syntax.BashPPSliceExpr:
	default:
		return nil, false
	}
	value, meta, err := r.bashPPReadExpr(expr)
	if err != nil || r.bashPPScope == nil {
		return nil, false
	}
	name := fmt.Sprintf("bashPPGoArg_%d_%d", uint(call.Pos().Offset()), i)
	if meta == nil {
		r.bashPPDeclareName(name, expand.Variable{Set: true, Kind: expand.String, Str: fmt.Sprint(value)})
	} else {
		// A slice shares the array it was taken from; its meta carries the
		// bounds. Re-basing the value would strand that meta, so the binding
		// keeps the value and identity the read produced.
		r.bashPPDeclareName(name, expand.NewObject(value))
		cell := r.bashPPScope.lookup(name)
		if cell == nil {
			return nil, false
		}
		cell.object = &bashPPObjectIdentity{owner: name, collection: meta}
		if root, ok := bashPPCollectionRoot(expr); ok {
			// The root binding can be aliased into a task; the identity is
			// taken from one snapshot of it rather than tested and then
			// re-read, which could carry a different store's identity.
			if source := r.bashPPScope.lookup(root).view(); source != nil && source.object != nil {
				cell.object = source.object
			}
		}
		cell.valueMeta = meta
	}
	return &syntax.Word{Parts: []syntax.WordPart{&syntax.Lit{
		Value: name, ValuePos: word.Pos(), ValueEnd: word.End(),
	}}}, true
}

// bashPPGoComputedArg reports an argument whose word text is not already its
// own value. A bare identifier keeps its established meaning — the binding path
// resolves it, and a channel argument must reach that path by name to carry its
// capability — so only a genuinely computed operand is evaluated here.
func bashPPGoComputedArg(expr syntax.BashPPExpr, word *syntax.Word) bool {
	if expr == nil || word == nil || len(word.Parts) != 1 {
		return false
	}
	if _, ok := word.Parts[0].(*syntax.Lit); !ok {
		return false
	}
	switch expr.(type) {
	case *syntax.BashPPIdent, *syntax.BashPPBasicLit:
		return false
	}
	return true
}
