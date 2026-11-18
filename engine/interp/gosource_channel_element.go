package interp

// Sprint: #118; Story: #67; Story-ID: 83b5cdc6fca6
import (
	"fmt"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// goSourceChannelType reports the channel shape expected holds, if any.
func (r *Runner) goSourceChannelType(expected syntax.BashPPTypeExpr) (*syntax.BashPPChanType, bool) {
	if !r.bashPPGoSource || expected == nil {
		return nil, false
	}
	// Collection shapes already carry their element as a structured type.
	// The common []chan T append path therefore needs no named-type walk;
	// avoiding it also avoids constructing the walk's cycle-detection map for
	// every appended element. Named channel types still resolve below.
	if typ, ok := expected.(*syntax.BashPPChanType); ok {
		return typ, true
	}
	typ, ok := r.bashPPUnderlyingType(expected).(*syntax.BashPPChanType)
	return typ, ok
}

// goSourceChannelElement evaluates one expression stored where a channel is
// expected: a struct field, a slice or array element, a map value.
//
// A channel is a reference, and until now only a variable could hold one — the
// identity lived on the cell, which a collection payload has no room for. Go
// programs put channels in structs constantly, and `readOp{resp: make(chan
// int)}` is the shape the whole request/response idiom is written in, so the
// identity travels beside the payload as element metadata instead.
//
// The two channel implementations diverge here. A dependency-owned channel is
// already a handle value and needs nothing extra; an interpreter-owned one has
// no value form at all, so its identity is what the metadata carries.
func (r *Runner) goSourceChannelElement(expr syntax.BashPPExpr, expected syntax.BashPPTypeExpr) (any, *bashPPCollectionMeta, bool, error) {
	if _, ok := r.goSourceChannelType(expected); !ok {
		return nil, nil, false, nil
	}
	meta := &bashPPCollectionMeta{kind: "channel", typ: expected}
	if id, ok := expr.(*syntax.BashPPIdent); ok && id.Name.Value == "nil" {
		return nil, meta, true, nil
	}
	cell, err := r.goSourceValueCell(expr)
	if err != nil {
		return nil, nil, true, err
	}
	if cell == nil {
		return nil, nil, true, fmt.Errorf("BASHPP-ECOLLECTION-ELEMENT: channel element has no value")
	}
	value, err := r.goSourceChannelCellValue(cell, expected, meta)
	return value, meta, true, err
}

// goSourceChannelCellValue reads the channel a cell names into the element
// payload and metadata a collection stores.
func (r *Runner) goSourceChannelCellValue(cell *bashPPCell, expected syntax.BashPPTypeExpr, meta *bashPPCollectionMeta) (any, error) {
	if native, ok := r.goSourceNativeChannel(cell); ok {
		return native, nil
	}
	if cell.channel != nil {
		// Preserve the reference beside the aggregate payload; send/receive
		// reconstruct a cell from this metadata without copying the channel.
		meta.channel, meta.channelOwner = cell.channel, cell.channelOwner
		return nil, nil
	}
	// A nil channel is still a channel: the declared type is what says so, and
	// the zero value carries no identity to check.
	if cell.vr.Kind != expand.Object && cell.vr.String() == "" {
		return nil, nil
	}
	return nil, fmt.Errorf("BASHPP-ECOLLECTION-ELEMENT: cannot use value as %s", bashPPTypeText(expected))
}

// goSourceCheckChannelValue admits the payloads goSourceChannelElement
// produces, and refuses anything else stored where a channel belongs.
func (r *Runner) goSourceCheckChannelValue(value any, meta *bashPPCollectionMeta, expected syntax.BashPPTypeExpr) error {
	if value == nil && (meta == nil || meta.channel == nil) {
		return nil
	}
	if native, ok := value.(*bashPPBridgeValue); ok && native != nil {
		return nil
	}
	if meta != nil && meta.channel != nil && r.bashPPTypeAssignable(meta.typ, expected) {
		return nil
	}
	return fmt.Errorf("BASHPP-ECOLLECTION-ELEMENT: cannot use value as %s", bashPPTypeText(expected))
}

// bashPPChannelElementEqual compares two channels read out of collections. Go
// compares channel values by identity, which for an interpreter-owned channel
// is the channel object and for a dependency-owned one is its handle.
func bashPPChannelElementEqual(left any, leftMeta *bashPPCollectionMeta, right any, rightMeta *bashPPCollectionMeta) bool {
	if leftMeta.channel != nil || rightMeta.channel != nil {
		return leftMeta.channel == rightMeta.channel
	}
	leftNative, leftOK := left.(*bashPPBridgeValue)
	rightNative, rightOK := right.(*bashPPBridgeValue)
	if !leftOK || !rightOK {
		return left == nil && right == nil
	}
	return leftNative.Handle == rightNative.Handle && leftNative.Kind == rightNative.Kind
}
