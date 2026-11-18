// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"go/constant"
	"strings"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Legacy receive operands still have compatibility words. GoSource sends use
// the positioned ChanExpr/ValueExpr trees handled by gosource_send.go; no shell
// expansion or reparsing of original Go expressions participates in that path.
// Classic Bash++ retains its established literal word semantics.

// bashPPGoRecvOperand answers the channel word of a receive spelled as a single
// word, `<-ch`. It recognizes only the identifier form, which is the only
// channel operand the concurrency runtime resolves; anything else keeps its
// literal meaning and is reported by the ordinary operand diagnostics.
func bashPPGoRecvOperand(w *syntax.Word) (*syntax.Word, bool) {
	if w == nil || len(w.Parts) != 1 {
		return nil, false
	}
	lit, ok := w.Parts[0].(*syntax.Lit)
	if !ok || !strings.HasPrefix(lit.Value, "<-") {
		return nil, false
	}
	name := strings.TrimSpace(strings.TrimPrefix(lit.Value, "<-"))
	if !syntax.BashPPValidIdent(name) {
		return nil, false
	}
	return &syntax.Word{Parts: []syntax.WordPart{&syntax.Lit{
		Value: name, ValuePos: w.Pos(), ValueEnd: w.End(),
	}}}, true
}

// bashPPChanElem reports the element type of the channel a word names without
// raising a diagnostic. The receive itself re-resolves the channel through the
// ordinary ownership check; this lookup only recovers the element type used to
// reconstruct the received scalar.
func (r *Runner) bashPPChanElem(chanWord *syntax.Word) string {
	if r.bashPPScope == nil {
		return ""
	}
	cell := r.bashPPScope.lookup(r.literal(chanWord)).view()
	if cell == nil || cell.channel == nil {
		return ""
	}
	return cell.channel.elem
}

// bashPPChanElemBase resolves a channel element type through declared aliases
// to the builtin it is ultimately spelled in, mirroring bashPPSetReceivedType
// so a received value and a received scalar agree on their carrier.
func (r *Runner) bashPPChanElemBase(elem string) (base, named string) {
	typ, ok := r.bashPPTypes[elem]
	if !ok {
		return elem, ""
	}
	for typ.alias {
		elem = typ.underlying
		typ, ok = r.bashPPTypes[elem]
		if !ok {
			return elem, ""
		}
	}
	return typ.underlying, elem
}

// bashPPChanElemKind is the constant kind a channel element carries. Unknown
// leaves the reconstruction to the textual heuristic, which is what a value of
// an aggregate or user-defined element type already relies on.
func (r *Runner) bashPPChanElemKind(base string) constant.Kind {
	switch {
	case base == "string":
		return constant.String
	case base == "bool":
		return constant.Bool
	case base == "float32" || base == "float64":
		return constant.Float
	case r.bashPPGoSource && (base == "complex64" || base == "complex128"):
		return constant.Complex
	case bashPPIntegerType(base):
		return constant.Int
	}
	return constant.Unknown
}

// bashPPChanValueCell rebuilds a received channel value as an ordinary cell so
// the established scalar reconstruction — not a second, divergent one — decides
// the value's kind and type identity.
func (r *Runner) bashPPChanValueCell(value, elem string) *bashPPCell {
	base, named := r.bashPPChanElemBase(elem)
	cell := &bashPPCell{vr: expand.Variable{Set: true, Kind: expand.String, Str: value}}
	cell.scalarKind = r.bashPPChanElemKind(base)
	if named != "" {
		cell.typeName = named
	} else if base != "" {
		cell.declType = &syntax.BashPPNamedType{Name: &syntax.Lit{Value: base}}
	}
	return cell
}

// bashPPGoReceiveScalar evaluates `<-ch` in expression position. The Go
// converter emits a receive expression as a unary operator, which has no
// arithmetic meaning; the operation is the runtime receive the concurrency
// group already implements, and its result is reconstructed as the element
// type the channel was made with.
func (r *Runner) bashPPGoReceiveScalar(x *syntax.BashPPUnaryExpr) (bashPPScalar, bool, error) {
	if !r.bashPPGoSource || x == nil || x.Op == nil || x.Op.Value != "<-" {
		return bashPPScalar{}, false, nil
	}
	if cell, ok := r.bashPPScalarReceiveMemo[x]; ok {
		delete(r.bashPPScalarReceiveMemo, x)
		return r.bashPPScalarFromCell(cell), true, nil
	}
	cell, _ := r.bashPPReceiveCell(r.ectx, &syntax.BashPPReceive{Arrow: x.Pos(), ChanExpr: x.X}, nil)
	if cell == nil {
		return bashPPScalar{}, true, errBashPPScalarInterrupted
	}
	return r.bashPPScalarFromCell(cell), true, nil
}

// bashPPGoReceiveWordValue performs a receive written as a bare word, which is
// how the Go converter carries the operands of a multi-value short declaration
// such as `x, y := <-c, <-c`.
func (r *Runner) bashPPGoReceiveWordValue(w *syntax.Word) (expand.Variable, bool) {
	if !r.bashPPGoSource {
		return expand.Variable{}, false
	}
	chanWord, ok := bashPPGoRecvOperand(w)
	if !ok {
		return expand.Variable{}, false
	}
	if r.bashPPScope == nil || r.bashPPScope.lookup(r.literal(chanWord)) == nil {
		return expand.Variable{}, false
	}
	value, _ := r.bashPPReceive(r.ectx, &syntax.BashPPReceive{Arrow: w.Pos(), Chan: chanWord}, nil)
	return expand.Variable{Set: true, Kind: expand.String, Str: value}, true
}

// bashPPGoSendValue evaluates the value half of a Go send. `c <- sum` sends the
// value bound to sum; the classic dialect's literal word is preserved outside a
// Go region, where a send spells its value with an ordinary expansion.
func (r *Runner) bashPPGoSendValue(w *syntax.Word) string {
	if !r.bashPPGoSource {
		return r.literal(w)
	}
	if vr, ok := r.bashPPGoReceiveWordValue(w); ok {
		return vr.Str
	}
	return r.bashPPExprValue(w)
}

// bashPPChanZeroText is the text a receive from a drained closed channel
// yields. Go delivers the element type's zero value, which for a numeric
// element is 0 rather than the empty string the carrier defaults to. Classic
// Bash++ keeps the empty carrier default it has always returned.
func (r *Runner) bashPPChanZeroText(elem string) string {
	if !r.bashPPGoSource {
		return ""
	}
	base, _ := r.bashPPChanElemBase(elem)
	switch r.bashPPChanElemKind(base) {
	case constant.Int:
		return "0"
	case constant.Float:
		return "0"
	case constant.Complex:
		return "(0+0i)"
	case constant.Bool:
		return "false"
	}
	return ""
}
