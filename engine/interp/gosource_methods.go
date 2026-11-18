package interp

import (
	"fmt"
	"go/types"
	"maps"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

type goSourceMethodBinding struct {
	receiver    *bashPPCell
	method      string
	selection   bashPPSelection
	addressable bool
}

func (r *Runner) goSourceLocalMethodValue(expr *syntax.BashPPSelectorExpr) (*bashPPCell, error) {
	fn, err := r.goSourceLocalMethod(expr, false)
	if err != nil {
		return nil, err
	}
	return &bashPPCell{vr: r.bashPPStoreFunc(fn), declType: expr.FuncType}, nil
}

func (r *Runner) goSourceLocalMethod(expr *syntax.BashPPSelectorExpr, afterArgs bool) (*bashPPFunc, error) {
	var receiver *bashPPCell
	if expr.ReceiverAddressable {
		// Taking the address evaluates an indexed/selected receiver once. Reading
		// its value and then taking its address would repeat index side effects.
		ptr, err := r.bashPPAddress(expr.X)
		if err != nil {
			return nil, err
		}
		// &*p may produce a nil address; evaluating (*p).M still
		// dereferences p before binding the method.
		if ptr == nil {
			return nil, r.goSourceRuntimeFaultAt(errBashPPNilDereference, expr.X)
		}
		_, pointer := r.bashPPPointerType(ptr.elem)
		_, iface := r.bashPPInterfaceType(ptr.elem)
		if native := r.bashPPEmbeddedNativeType(ptr.elem); native != nil {
			_, iface = native.Underlying().(*types.Interface)
		}
		if !pointer && !iface {
			// An addressable concrete receiver needs only its address/type.
			// Reading and validating a temporary object here would traverse
			// mutable map/slice fields before its method acquires a lock.
			receiver = bashPPPointerCell(ptr)
		} else {
			value, meta, typ, err := ptr.read()
			if err != nil {
				return nil, err
			}
			receiver = &bashPPCell{declType: typ}
			bashPPStoreCellValue(receiver, value, meta)
			// A pointer straight at a binding can name a cell a task shares,
			// so the payload word decides through the guard.
			if len(ptr.path) == 0 && ptr.target.viewInterface() != nil {
				receiver = ptr.target
			}
		}
	} else {
		var err error
		receiver, err = r.goSourceValueCell(expr.X)
		if err != nil {
			return nil, err
		}
	}
	var fn *bashPPFunc
	var ok bool
	// A computed receiver the dependency owns binds its method there; see
	// bashpp_sprint165_runtime_panic.go.
	if native, claimed, err := r.goSourceNativeReceiverMethod(receiver, expr.Sel.Value); claimed {
		return native, err
	}
	// The receiver can be the shared cell itself, so the payload is read once
	// and the method binds against THAT payload rather than re-reading a word
	// a concurrent rebinding may already have replaced.
	if iface := receiver.viewInterface(); iface != nil {
		// The interface lookup faults at the method name, including
		// selectors whose dot and method name occupy different lines.
		oldPos := r.curStmtPos
		r.curStmtPos = expr.Sel.Pos()
		defer func() { r.curStmtPos = oldPos }()
		fn, ok = r.bashPPBindInterfaceMethod(iface, expr.Sel.Value)
	} else {
		typ := bashPPSelectorCellType(receiver)
		sel := r.bashPPResolveSelection(typ, expr.Sel.Value, true, expr.ReceiverAddressable)
		if sel.ambiguous || sel.method == nil && sel.interfaceSpec == nil {
			return nil, fmt.Errorf("gosource: cannot resolve method %s on %s", expr.Sel.Value, bashPPTypeText(typ))
		}
		if afterArgs && sel.method != nil && !sel.method.decl.Receiver.Pointer {
			// Preserve the evaluated address until arguments have completed.
			// Even a discarded eager value copy would read user storage before
			// an argument's synchronization or mutation has taken place.
			copy := *sel.method
			copy.receiver = receiver
			copy.typeArgs = bashPPMethodTypeBindings(sel.method, sel.receiverType)
			copy.goSourceReceiver = &goSourceMethodBinding{receiver: receiver, method: expr.Sel.Value, selection: sel, addressable: expr.ReceiverAddressable}
			fn, ok = &copy, true
		} else {
			fn, ok = r.bashPPBindPromotedMethod(receiver, expr.Sel.Value, sel, expr.ReceiverAddressable)
		}
	}
	if !ok {
		return nil, errBashPPScalarInterrupted
	}
	return fn, nil
}

// Called after arguments, before either invocation or deferred/task capture.
// The evaluated pointer keeps its identity; the value receiver is copied now.
func (r *Runner) goSourceFinalizeReceiver(fn *bashPPFunc) bool {
	if fn == nil || fn.goSourceReceiver == nil {
		return true
	}
	binding := fn.goSourceReceiver
	fn.goSourceReceiver = nil
	bound, ok := r.bashPPBindPromotedMethod(binding.receiver, binding.method, binding.selection, binding.addressable)
	if ok {
		fn.receiver = bound.receiver
		if len(fn.typeArgs) == 0 {
			fn.typeArgs = bound.typeArgs
		} else if len(bound.typeArgs) > 0 {
			// Instantiation happens before argument evaluation. Finalizing an
			// eagerly captured value receiver after those arguments must add
			// its receiver bindings without discarding the generic method's
			// own bindings.
			merged := make(map[string]syntax.BashPPTypeExpr, len(bound.typeArgs)+len(fn.typeArgs))
			maps.Copy(merged, bound.typeArgs)
			maps.Copy(merged, fn.typeArgs)
			fn.typeArgs = merged
		}
	}
	return ok
}
