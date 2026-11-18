package interp

import (
	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Method selection is an operand of go: bind once before arguments and carry
// an ephemeral callable into this task. Publishing it in the parent's closure
// registry would accumulate receivers and retain dead sessions after Reset.
func (r *Runner) goSourceTaskMethodPin(call *syntax.BashPPCall, fn *bashPPFunc) (*bashPPFunc, *bashPPGoSourcePin) {
	if fn == nil || fn.native != nil || fn.decl == nil || fn.decl.Receiver == nil || fn.receiver == nil {
		return nil, nil
	}
	copy := *fn
	receiver := fn.receiver
	if fn.decl.Receiver.Pointer && !receiver.pointer {
		// The implicit address is a receiver VALUE. Reassigning the method's
		// receiver parameter must never rebind the caller's original variable.
		copy.receiver = bashPPPointerCell(&bashPPPointer{target: receiver, elem: receiver.declType})
	} else {
		copy.receiver = goSourceCopyTaskReceiver(receiver)
	}
	return &copy, &bashPPGoSourcePin{call: call, bound: &copy}
}

func (r *Runner) goSourceCopyTaskMethodPin(pin *bashPPGoSourcePin, shared map[*bashPPCell]bool) *bashPPGoSourcePin {
	cloner := newBashPPClonerFor(r)
	cloner.shared = shared
	cloner.goSourceTask = true
	method := *pin.bound
	method.scope = cloner.clone(method.scope)
	method.receiver = goSourceCopyTaskReceiver(method.receiver)
	copy := *pin
	copy.bound = &method
	return &copy
}

// A method value held in a function variable still passes a fresh receiver
// parameter on each launch. It must not mutate the stored method binding.
func (r *Runner) goSourcePinTaskCallable(call *syntax.BashPPCall, fn *bashPPFunc, handle string) (*bashPPFunc, *bashPPGoSourcePin) {
	if fn != nil && fn.decl != nil && fn.decl.Receiver != nil && fn.receiver != nil {
		return r.goSourceTaskMethodPin(call, fn)
	}
	return fn, &bashPPGoSourcePin{call: call, handle: handle}
}

func goSourceCopyTaskReceiver(source *bashPPCell) *bashPPCell {
	if source == nil {
		return nil
	}
	copy := *source.view()
	if source.vr.Kind == expand.Object && bashPPValueMeta(bashPPCellMeta(source)) {
		value, meta := bashPPCopyArrayValue(source.vr.Obj, bashPPCellMeta(source))
		copy.vr = expand.Variable{Set: true, Kind: expand.Object, Obj: value}
		copy.valueMeta = meta
		copy.object = &bashPPObjectIdentity{collection: meta}
	}
	return &copy
}
