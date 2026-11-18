package interp

import (
	"fmt"
	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Every Go source make(chan) route returns the same typed capability cell.
// Capacity is evaluated only in the selected allocation path.
func (r *Runner) goSourceMakeChannelCell(declared syntax.BashPPTypeExpr, expr syntax.BashPPExpr, word *syntax.Word) (*bashPPCell, error) {
	typ, ok := r.bashPPUnderlyingType(declared).(*syntax.BashPPChanType)
	if !ok {
		return nil, fmt.Errorf("make(chan): missing channel type")
	}
	if cell, handled, err := r.goSourceMakeNativeChannel(typ, expr, word, declared); handled {
		return cell, err
	}
	capacity, err := r.goSourceChannelCapacity(expr, word)
	if err != nil {
		return nil, err
	}
	// The runtime's own refusal of the storage actually allocated must unwind
	// as the recoverable runtime panic makechan reports in the original
	// program, never as a host panic out of the interpreter's own allocation.
	channel, err := newBashPPChannelChecked(bashPPTypeText(typ.Element), capacity)
	if err != nil {
		r.bashPPRaise(err.Error())
		return nil, errBashPPScalarInterrupted
	}
	group := r.bashPPConcurrency(r.ectx)
	handle, err := newBashPPChannelCapability()
	if err != nil {
		return nil, fmt.Errorf("make(chan): cannot allocate capability: %w", err)
	}
	channel.element = typ.Element
	group.mu.Lock()
	group.chans[handle] = channel
	group.mu.Unlock()
	return &bashPPCell{vr: expand.Variable{Set: true, Kind: expand.String, Str: handle}, declType: declared, channel: channel, channelOwner: group}, nil
}

// A channel declaration must retain its cell, not the legacy initializer word.
// This applies equally to package and local vars and does not run any original
// body in the dependency process.
func (r *Runner) goSourceChannelDeclaration(d *syntax.BashPPDecl) bool {
	if !r.bashPPGoSource || d.Site != syntax.StartVar || d.DeclTypeExpr == nil {
		return false
	}
	if _, ok := r.bashPPUnderlyingType(d.DeclTypeExpr).(*syntax.BashPPChanType); !ok {
		return false
	}
	cell := goSourceNativeValueCell(bashPPBridgeValue{Kind: "nil", Type: r.bashPPBridgeTypeIdentity(d.DeclTypeExpr)})
	if typ, ok := r.bashPPUnderlyingType(d.DeclTypeExpr).(*syntax.BashPPChanType); ok && typ.LocalDomain {
		// A nil channel has no allocation to place, but its select arm must
		// still belong to the package-planned domain of the channels it can
		// arbitrate beside.
		cell = &bashPPCell{vr: expand.Variable{Set: true, Kind: expand.String}}
	}
	cell.declType = d.DeclTypeExpr
	if d.InitExpr != nil && !goSourceNilLiteral(d.InitExpr) {
		value, err := r.goSourceValueCell(d.InitExpr)
		if err != nil {
			r.bashPPGoSendError(d.InitExpr, err)
			return true
		}
		cell = bashPPCopyAssignmentCell(value)
	}
	if cell == nil {
		r.exit.fatal(fmt.Errorf("channel initializer returned no value"))
		return true
	}
	if native, ok := r.goSourceNativeChannel(cell); ok && native.Kind != "nil" {
		// Channel assignment may narrow direction or introduce a defined
		// type. Preserve that static type when the value crosses an interface
		// boundary, while retaining the exact same underlying Go channel.
		// A zero channel has no underlying identity to convert and may mention
		// a local element type that the dependency need never materialise. Its
		// declaration cell already carries the authoritative static type.
		bound, err := r.bashPPNativeTypeRequest("channel-bind", d.DeclTypeExpr, *native)
		if err != nil {
			r.exit.fatal(err)
			return true
		}
		cell = goSourceNativeValueCell(bound)
	}
	cell.declType = d.DeclTypeExpr
	if named, ok := d.DeclTypeExpr.(*syntax.BashPPNamedType); ok {
		cell.typeName = named.Name.Value
	}
	if d.Name.Value == "_" {
		return true
	}
	if err := r.bashPPScope.declare(d.Name.Value, cell.vr, false); err != nil {
		r.exit.fatal(err)
		return true
	}
	r.bashPPScope.lookup(d.Name.Value).publish(cell)
	return true
}
