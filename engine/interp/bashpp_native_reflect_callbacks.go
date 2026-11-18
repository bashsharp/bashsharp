package interp

import (
	"strings"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Sprint: #248; Story: #701; Story-ID: 9464cf0df26e
//
// A reflected method expression over a local type.
//
// reflect.TypeOf(x).Method(i).Func.Interface() yields a native func whose
// first parameter is the receiver, e.g. func(*T). Calling it with an
// interpreter-owned pointer is refused by the general transport rule, because
// an arbitrary native func could retain that pointer or write through a
// decoded copy. The worker can prove the handle is not arbitrary: its code
// identity and signature equal Type.Method(i).Func of a type whose method is a
// generated mirror trampoline (mirroredMethodExpression). Such a func runs no
// dependency code — it carries the call straight back to the original body —
// so the shared-reference invariants of nativeSharedReferenceConsumer hold:
// an origin-bearing pointer binds the ORIGINAL storage in the callback, the
// callback reply re-decodes the pointee, and a (never expected) native write
// still rides the pointer writeback. Every other interpreter-owned reference
// shape keeps the refusal.

// mirroredMethodExpressionCall reports a bare call of a native function handle
// the worker proves to be a mirrored method expression, whose every argument
// is either an origin-bearing pointer of this session or reference-free data.
func mirroredMethodExpressionCall(req bashPPEvalRequest, q bashPPBridgeRequest) bool {
	if q.Op != "call" || q.Selector != "" || q.Receiver == nil || q.Receiver.Kind != "handle" || !q.Receiver.Function {
		return false
	}
	if req.CallbackOwner == nil || req.Bridge == nil || len(q.Args) == 0 {
		return false
	}
	for _, arg := range q.Args {
		if arg.Kind == "pointer" {
			if arg.Origin == 0 || arg.Session != req.Bridge.id {
				return false
			}
			continue
		}
		if bridgeValueHasReference(arg) {
			return false
		}
	}
	values, err := req.CallbackOwner.bashPPNativeRequest(req.CallbackOwner.ectx, req, bashPPBridgeRequest{Op: "mirrored-method-expression", Receiver: q.Receiver})
	if err != nil {
		return false
	}
	return len(values) == 1 && values[0].Kind == "bool" && values[0].Text == "true"
}

// bridgeValueHasReference reports whether a transported value is or contains
// anything but plain data: a pointer, slice, map, handle or callback.
func bridgeValueHasReference(v bashPPBridgeValue) bool {
	switch v.Kind {
	case "string", "bool", "int", "uint", "float", "complex", "struct", "array":
	default:
		return true
	}
	if v.Origin != 0 || v.Callbacks || v.sliceView != nil || len(v.Entries) > 0 {
		return true
	}
	for _, child := range v.Elements {
		if bridgeValueHasReference(child) {
			return true
		}
	}
	for _, child := range v.Fields {
		if bridgeValueHasReference(child) {
			return true
		}
	}
	return false
}

// bashPPReflectValueSnapshot gives reflect.ValueOf over a NON-addressable
// value of a local method-bearing type (reflect.ValueOf(T{}),
// reflect.ValueOf(T(0))) the cell Go itself creates for it: ValueOf copies its
// operand into an interface, so the reflected value — and every method value
// taken from it — is bound to that private copy, never to storage the program
// can name. The copy is fresh and unaliased, so it has the same authenticated
// route an addressable local has, and the worker snapshots the value receiver
// exactly as it does for one (see bashPPReflectValueReceiver). Only plain data
// qualifies: a copy sharing a slice, map or pointer with the caller is left to
// the general refusal.
func (r *Runner) bashPPReflectValueSnapshot(req bashPPEvalRequest, arg bashPPBridgeValue) *bashPPCell {
	if req.Bridge == nil || arg.Kind == "pointer" || arg.Kind == "nil" || bridgeValueHasReference(arg) {
		return nil
	}
	typeName := strings.TrimPrefix(arg.Type, "main.")
	for _, local := range req.localTypePlan().transported(typeName) {
		if len(local.Methods) == 0 {
			continue
		}
		named := &syntax.BashPPNamedType{Name: &syntax.Lit{Value: typeName}}
		value, meta, err := r.bashPPBridgeContents(arg, named)
		if err != nil {
			return nil
		}
		cell := &bashPPCell{declType: named, typeName: typeName}
		bashPPStoreCellValue(cell, value, meta)
		return cell
	}
	return nil
}

// bashPPReflectMakeFuncShape reports an original function of exactly the
// reflect.MakeFunc implementation type, func([]reflect.Value) []reflect.Value.
// Both slices hold only dependency handles, and the one consumer that invokes
// this shape — reflect's callReflect — allocates the argument slice afresh for
// every call, never reads it after the body returns, and copies each result
// out before returning. A transported copy of either slice therefore observes
// exactly what native Go observes. Any other slice-typed callback keeps the
// value-semantics refusal.
func (r *Runner) bashPPReflectMakeFuncShape(fn *bashPPFunc) bool {
	params, results := bashppParams(fn.params()), bashppParams(fn.results())
	if len(params) != 1 || len(results) != 1 || params[0].variadic {
		return false
	}
	isValues := func(typ syntax.BashPPTypeExpr) bool {
		shape, ok := typ.(*syntax.BashPPCollectionType)
		if !ok || shape.Kind != "slice" {
			return false
		}
		named, ok := shape.Element.(*syntax.BashPPNamedType)
		if !ok {
			return false
		}
		alias, name, ok := strings.Cut(named.Name.Value, ".")
		return ok && name == "Value" && r.bashPPImports[alias] == "reflect"
	}
	return isValues(params[0].typ) && isValues(results[0].typ)
}

// reflect.MakeFunc over an original implementation.
//
// MakeFunc retains its implementation (retainedFunctionCallback), so the
// session serves that callback from every later request. Its result — a
// reflect.Value, and the func Interface() views it as — carries the callback
// taint, and would otherwise be refused on every use. Two uses are admitted,
// and only on handles this session recorded as made functions:
//   - Interface() on the made reflect.Value, whose result is the same function;
//   - a bare call of the made function, whose arguments are plain data or
//     dependency handles. The call parks and runs the implementation
//     synchronously, exactly as native Go's callReflect does.
// Handing a made function to any other dependency API keeps the refusal.

// rememberMadeFunc records a handle result of reflect.MakeFunc with an
// original implementation, or of Interface() on such a handle.
func (s *bashPPNativeSession) rememberMadeFunc(req bashPPEvalRequest, q bashPPBridgeRequest, v bashPPBridgeValue) {
	if v.Kind != "handle" || v.Handle == 0 || q.Op != "call" {
		return
	}
	made := false
	owner := req.CallbackOwner
	if q.Receiver == nil {
		alias, name, ok := strings.Cut(q.Selector, ".")
		made = ok && name == "MakeFunc" && req.Imports[alias] == "reflect" && requestHasCallbacks(req, q)
	} else {
		made = q.Selector == "Interface" && len(q.Args) == 0 && s.madeFunc(*q.Receiver)
		owner = s.madeFuncOwner(*q.Receiver)
	}
	if !made {
		return
	}
	s.mu.Lock()
	if s.madeFuncs == nil {
		s.madeFuncs = map[uint64]*Runner{}
	}
	s.madeFuncs[v.Handle] = owner
	s.mu.Unlock()
}

func (s *bashPPNativeSession) madeFunc(v bashPPBridgeValue) bool {
	if s == nil || v.Kind != "handle" || v.Session != s.id {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.madeFuncs[v.Handle]
	return ok
}

func (s *bashPPNativeSession) madeFuncOwner(v bashPPBridgeValue) *Runner {
	if s == nil || v.Kind != "handle" || v.Session != s.id {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.madeFuncs[v.Handle]
}

// A retained implementation is served only by the runner which created it.
// That runner owns the callback's lexical cells and can park its request to
// execute the original body. Another task's request cannot borrow that frame.
func (s *bashPPNativeSession) madeFuncOwnedBy(v bashPPBridgeValue, owner *Runner) bool {
	return owner != nil && s.madeFuncOwner(v) == owner
}

// madeFuncCallable reports a made handle a runner may call or view through
// Interface. Any runner may: a bare call is a routed request
// (routedCallbackRequest), so the implementation runs on the calling runner's
// own parked request — as native callReflect runs it on the calling goroutine
// — and its closure reaches the captured cells it shares with every task.
func (s *bashPPNativeSession) madeFuncCallable(v bashPPBridgeValue, owner *Runner) bool {
	return owner != nil && s.madeFunc(v)
}

// routedCallbackRequest reports a request whose callbacks are delivered to it
// by request identity instead of through the shared callback gate: a bare call
// of a made function, and every callback-capable request a callback served
// that way raises. Such callbacks run only on their own dispatch goroutine in
// the helper, so no other request can be handed them, and one task blocking
// inside an implementation never holds the gate the others need.
func routedCallbackRequest(req bashPPEvalRequest, q bashPPBridgeRequest) bool {
	owner := req.CallbackOwner
	if owner == nil || req.Bridge == nil {
		return false
	}
	if owner.bashPPTools.routedDepth > 0 {
		return requestCallbackCapable(req, q)
	}
	// A task or subshell created by an original callback owns a cloned Runner. Its
	// synchronous callbacks must return to its own parked request: borrowing
	// the outer Runner would race its interpreter state, while waiting for the
	// shared callback gate can deadlock when the outer callback joins the child.
	// The helper's authenticated request route provides that ownership. Keep an
	// unparented retained callback on the shared gate; it has no route parent and
	// still belongs to the serialized outer callback owner.
	if owner.bashPPTools.callbackDescendant {
		return requestCallbackCapable(req, q)
	}
	return q.Op == "call" && q.Selector == "" && q.Receiver != nil && q.Receiver.Function && req.Bridge.madeFuncCallable(*q.Receiver, owner)
}

// bashPPMadeFuncOwner reports a request with a callback runner. Its identity
// is checked against each made handle before using that handle.
func bashPPMadeFuncOwner(req bashPPEvalRequest) bool {
	return req.CallbackOwner != nil
}

// bashPPMadeFuncUse reports one of the two admitted uses of a made function.
func bashPPMadeFuncUse(req bashPPEvalRequest, q bashPPBridgeRequest) bool {
	if q.Op != "call" || q.Receiver == nil || !req.Bridge.madeFuncCallable(*q.Receiver, req.CallbackOwner) {
		return false
	}
	switch q.Selector {
	case "Interface":
		return len(q.Args) == 0
	case "":
		if !q.Receiver.Function {
			return false
		}
		for _, arg := range q.Args {
			if arg.Kind == "handle" && (!arg.Callbacks || req.Bridge.madeFunc(arg)) {
				continue
			}
			if bridgeValueHasReference(arg) {
				return false
			}
		}
		return true
	}
	return false
}

// bashPPCallbackRaised reports whether an original callback body, entered at
// call-stack depth entry, ended by raising a panic the dependency must see.
//
// A callback can run as a cleanup of a frame that is already unwinding: a
// deferred call of a native function (a reflect.MakeFunc result, a reflected
// method) re-enters the interpreter while that frame's panic is still active
// at depth entry. Such a body may return normally without recovering it, and
// that outer panic is then not the callback's outcome — it keeps unwinding
// the frame after the deferred call returns. Only a panic raised by the body
// itself or deeper (its depth beyond entry) crosses back to the dependency.
func (r *Runner) bashPPCallbackRaised(entry int) bool {
	if !r.bashPPPanicking() {
		return false
	}
	depths := r.bashPPPanic.depths
	return len(depths) == 0 || depths[len(depths)-1] > entry
}

// reflectedHandleOperand reports a request operating on a handle minted along
// the reviewed reflect.ValueOf chain (its Origin names the reflected local
// value). Any dependency operation over such a handle — fmt formatting it,
// Interface(), Call — may raise the local type's mirrored methods, so the
// request must park as a callback server; otherwise a String or Error
// callback finds no owner and its failure is printed as the value's text.
func reflectedHandleOperand(q bashPPBridgeRequest) bool {
	if q.Receiver != nil && q.Receiver.Kind == "handle" && q.Receiver.Origin != 0 {
		return true
	}
	for _, arg := range q.Args {
		if arg.Kind == "handle" && arg.Origin != 0 {
			return true
		}
	}
	return false
}
