package interp

// Sprint: #243; Story: #675; Story-ID: 6ee00d8029b3
//
// Exclusive slice transfer across the dependency bridge.
//
// The bridge transports a slice by copying its elements, so a dependency that
// RETAINS the decoded slice header past the call — testing.MainStart stores
// its four descriptor slices directly in the *M it returns — would keep a
// backing array the interpreter's own aliases no longer share: an element
// replacement made later on either side would be invisible to the other. The
// copied-reference refusal in prepareNativeSliceBuffers guards exactly that,
// and a callee name, a descriptor shape or native element handles prove
// nothing about it: they authenticate the elements, not the backing array.
//
// A slice whose backing array has no interpreter alias other than the one
// binding that supplies it can instead be TRANSFERRED. The decoded native
// slice becomes the storage of record — the callee retains that very header —
// and the supplying binding is rebound to a handle on the same slice, so every
// later read the program makes through it (len, index, range, slicing)
// observes the dependency's retained backing, and an element write through
// it is refused by the handle path rather than silently diverging. What the
// transfer needs is therefore an exclusivity proof for the backing array. The
// proof is produced by the Go front end after inspecting the whole package:
// a package variable must be initialized by a fresh slice literal and its
// checker object must occur exactly once, as this bare call argument. The
// backend-asserted test-main fact is separate required authority and is never
// inferred from a file name or suffix. A call site outside the program package
// (the test package's
// own sources run under the same fact), an argument that is not the bare
// binding, and an element that is itself interpreter-owned storage (a
// pointer, a nested slice or map, a local struct, a bare callback) keep the
// refusal. A static single-use proof from the front end would be a second
// admissible source; nothing here keys on the callee.

import (
	"fmt"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// bashPPNativeArgCells records, per argument, the binding a bare identifier
// argument's original slice was read from — the interpreter's own alias of
// that backing array, which a transfer rebinds to the dependency's retained
// slice. Only an argument spelled as the binding itself qualifies: a
// sub-slice, an append result or any other expression is a distinct header
// the transfer could not rebind. The recorded cell is verified to hold the
// very backing array the transported view captured.
func (r *Runner) bashPPNativeArgCells(exprs []syntax.BashPPExpr, args []bashPPBridgeValue) []*bashPPCell {
	var cells []*bashPPCell
	for i, expr := range exprs {
		if i >= len(args) || args[i].sliceView == nil || r.bashPPScope == nil {
			continue
		}
		for {
			paren, ok := expr.(*syntax.BashPPParenExpr)
			if !ok {
				break
			}
			expr = paren.X
		}
		ident, ok := expr.(*syntax.BashPPIdent)
		if !ok || ident.Name == nil {
			continue
		}
		cell := r.bashPPScope.lookup(ident.Name.Value)
		if cell == nil {
			continue
		}
		// The identity test below compares the binding's CURRENT backing array
		// with the one the bridge captured, so every field it screens on has to
		// come from the same store; the cell itself is still what is recorded.
		snapshot := cell.view()
		if snapshot.pointer || snapshot.interfaceValue != nil || snapshot.vr.Kind != expand.Object {
			continue
		}
		view, ok := snapshot.vr.Obj.([]any)
		captured := args[i].sliceView.view
		if !ok || len(view) != len(captured) || len(view) > 0 && &view[0] != &captured[0] {
			continue
		}
		if cells == nil {
			cells = make([]*bashPPCell, len(exprs))
		}
		cells[i] = cell
	}
	return cells
}

// bridgeValueDependencyOwned reports a transported value that is not a copy
// of interpreter storage: a dependency handle, a scalar or nil. A bare
// callback, an origin-bearing value and every structured shape (pointer,
// slice, array, map, struct) are interpreter-owned.
func bridgeValueDependencyOwned(v bashPPBridgeValue) bool {
	if v.Origin != 0 || v.sliceView != nil || len(v.Elements) > 0 || len(v.Fields) > 0 || len(v.Entries) > 0 {
		return false
	}
	switch v.Kind {
	case "handle", "nil", "string", "bool", "int", "uint", "float", "complex":
		return true
	}
	return false
}

// bridgeValueCallbacks reports whether a transported value or anything
// inside it carries original callbacks.
func bridgeValueCallbacks(v bashPPBridgeValue) bool {
	if v.Callbacks || v.Kind == "callback" {
		return true
	}
	for _, e := range v.Elements {
		if bridgeValueCallbacks(e) {
			return true
		}
	}
	for _, e := range v.Fields {
		if bridgeValueCallbacks(e) {
			return true
		}
	}
	for _, e := range v.Entries {
		if bridgeValueCallbacks(e.Key) || bridgeValueCallbacks(e.Value) {
			return true
		}
	}
	return false
}

// nativeSliceTransferable reports whether every original slice this request
// carries may be transferred, recording the transferred argument indexes on
// the request. The caller consults it only where the copied-reference
// refusal would otherwise fire, so it never widens what a callback-free or
// read-only request already does.
func nativeSliceTransferable(req bashPPEvalRequest, q *bashPPBridgeRequest) bool {
	q.Transfers = nil
	// The exclusivity proof: the backend-asserted generated test main, at a
	// call site in the program package itself.
	if !req.TestMain || !q.sourceProgram || q.Op != "call" {
		return false
	}
	if q.Receiver != nil && (q.Receiver.sliceView != nil || nestedNativeSliceView(*q.Receiver)) {
		return false
	}
	var transfers []int
	seenCells := make(map[*bashPPCell]bool)
	for i := range q.Args {
		arg := q.Args[i]
		if nestedNativeSliceView(arg) {
			return false
		}
		if arg.sliceView == nil {
			continue
		}
		if i >= len(q.argCells) || q.argCells[i] == nil {
			return false
		}
		if i >= len(q.transferProof) || !q.transferProof[i] {
			return false
		}
		if seenCells[q.argCells[i]] {
			return false
		}
		seenCells[q.argCells[i]] = true
		for _, elem := range arg.Elements {
			if !bridgeValueDependencyOwned(elem) {
				return false
			}
		}
		transfers = append(transfers, i)
	}
	if len(transfers) == 0 {
		return false
	}
	q.Transfers = transfers
	// The worker retains the decoded slice as the storage of record past
	// the call, so the supplying regions stay pinned.
	if req.Bridge != nil {
		req.Bridge.mu.Lock()
		for _, i := range transfers {
			req.Bridge.pinSliceRegionLocked(q.Args[i].Storage)
		}
		req.Bridge.mu.Unlock()
	}
	return true
}

// nativeSliceTransferOnly reports a transfer request whose every other
// argument, and receiver if any, is dependency-owned already: after the
// transfer the dependency holds no copy of interpreter storage at all, which
// is what the transport refusal on dependency mutation protects.
func nativeSliceTransferOnly(q bashPPBridgeRequest) bool {
	if len(q.Transfers) == 0 {
		return false
	}
	transferred := map[int]bool{}
	for _, i := range q.Transfers {
		transferred[i] = true
	}
	for i, arg := range q.Args {
		if transferred[i] {
			continue
		}
		if !bridgeValueDependencyOwned(arg) {
			return false
		}
	}
	return q.Receiver == nil || bridgeValueDependencyOwned(*q.Receiver)
}

// applyNativeSliceTransfers rebinds each transferred argument's supplying
// variable to the handle the worker minted on the decoded slice, so the
// interpreter's binding and the dependency's retained header share one
// backing array from here on. The handle keeps the callback-bearing mark the
// transported elements carried.
func (s *bashPPNativeSession) applyNativeSliceTransfers(q bashPPBridgeRequest, reply bashPPBridgeResponse) error {
	if len(q.Transfers) == 0 && len(reply.Transferred) == 0 {
		return nil
	}
	if len(reply.Transferred) != len(q.Transfers) {
		return fmt.Errorf("gosource: native slice transfer count mismatch")
	}
	for i, wire := range reply.Transferred {
		index := q.Transfers[i]
		if wire.Index != index || index < 0 || index >= len(q.Args) || index >= len(q.argCells) || q.argCells[index] == nil ||
			wire.Value.Kind != "handle" || wire.Length != len(q.Args[index].Elements) {
			return fmt.Errorf("gosource: invalid native slice transfer shape")
		}
		handle := wire.Value
		s.bashPPAuthenticateCallbackValue(&handle)
		handle.Callbacks = bridgeValueCallbacks(q.Args[index])
		bashPPRebindTransferredCell(q.argCells[index], handle)
	}
	return nil
}

// bashPPRebindTransferredCell makes the binding a dependency handle exactly
// as bashPPBindNativeValue binds a native result, keeping the alias identity
// readonly tracks. The declared collection layout is gone with the storage.
func bashPPRebindTransferredCell(cell *bashPPCell, value bashPPBridgeValue) {
	copy := value
	// The surviving identity edge is read from a snapshot and the whole new
	// binding is published as one store; see bashpp_cell_share.go.
	object := cell.view().object
	cell.publish(&bashPPCell{vr: expand.NewObject(&copy), object: object, typeName: value.Type})
}

// Sprint: #247; Story: #673; Story-ID: f24307569417
//
// Shared reference provenance for a synchronous methods-driven consumer.
//
// The general refusal on dependency mutation exists because a decoded copy of
// interpreter storage has no write-back contract: a native write to the copy
// would be invisible to the original, and an original callback would run
// against storage the copy no longer matches. Both halves of that hazard are
// closed for an argument that crosses as an ORIGIN-BEARING POINTER handed to a
// reviewed consumer that keeps every data access inside the call:
//
//   - ownership: the worker binds the origin to one stable native pointee
//     (originalPointers), so every native read the consumer makes goes through
//     the same storage the session reconciles — not an anonymous copy;
//   - callback coherence: a mirrored method callback binds the ORIGINAL
//     interpreter storage (bashPPNativeCallback resolves the origin to the
//     original pointer cell), and the callback reply re-decodes the receiver
//     into the origin's native pointee, so the consumer never observes a
//     pointee that is stale against an effect its own callback performed;
//   - write-back: a native write the consumer itself makes through the pointee
//     is detected against the last reconciled snapshot and transported back by
//     the pointer writeback (PtrUpdates), so no dependency write is dropped.
//
// The one admitted operation is text/template's synchronous execution over a
// worker-inspected callback-free tree. It does not retain the data argument;
// field reads and niladic method calls remain inside the parked request. Every
// other consumer keeps the refusal.

// nativeSharedReferenceConsumer reports a reviewed synchronous consumer whose
// every interpreter-owned reference argument carries original identity. The
// template consumer additionally requires the worker-inspected function-free
// template proof, which needs a bridge round trip and therefore runs in
// nativeTemplateExecuteProven.
func nativeSharedReferenceConsumer(req bashPPEvalRequest, q bashPPBridgeRequest) bool {
	if q.Op != "call" {
		return false
	}
	switch nativeSliceCallable(req, q) {
	case "*text/template.Template.Execute":
		// Execute(wr, data): the writer must already be the dependency's own
		// storage; only the data argument may carry original identity.
		if q.Receiver == nil || q.Receiver.Kind != "handle" || len(q.Args) != 2 || !bashPPDependencyOwnedWriter(q.Args[:1]) {
			return false
		}
	default:
		return false
	}
	shared := false
	for _, arg := range q.Args {
		if bridgeValueDependencyOwned(arg) {
			continue
		}
		// An origin-bearing pointer is the one interpreter-owned shape with a complete
		// ownership/write-back story; anything else — a detached copy of a
		// slice, struct or map, a bare callback — keeps the refusal.
		if arg.Kind == "pointer" && arg.Origin != 0 {
			shared = true
			continue
		}
		return false
	}
	return shared
}

// nativeTemplateExecuteProven adds the template consumer's remaining proof: a
// parsed tree free of function identifiers and template invocations, verified
// by the worker against the very template the handle names. Field access on
// the data pointee reads the origin-bound storage the session reconciles, and
// a niladic method spelling runs the mirrored original body — both coherent
// under the invariants above. A function or sub-template is an escape the
// review does not cover.
func nativeTemplateExecuteProven(req bashPPEvalRequest, q bashPPBridgeRequest) bool {
	if nativeSliceCallable(req, q) != "*text/template.Template.Execute" || req.CallbackOwner == nil {
		return false
	}
	values, err := req.CallbackOwner.bashPPNativeRequest(req.CallbackOwner.ectx, req, bashPPBridgeRequest{Op: "template-readonly", Receiver: q.Receiver})
	if err != nil {
		return false
	}
	return len(values) == 1 && values[0].Kind == "bool" && values[0].Text == "true"
}
