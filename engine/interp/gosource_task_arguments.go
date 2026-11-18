package interp

import (
	"context"
	"fmt"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// A launched call owns argument VALUES, not the caller's variable bindings.
// Assignment copies preserve pointee/map/slice/channel identity while copying
// structs and arrays. Keeping these values outside the shell task snapshot is
// essential: that snapshot deliberately deep-clones Classic Bash++ state.
type goSourceTaskArguments struct {
	pin        *bashPPGoSourcePin
	args       []string
	cells      []*bashPPCell
	channels   []*bashPPChannel
	interfaces []*bashPPInterfaceValue
}

func (r *Runner) goSourcePrepareTaskArguments(call *syntax.BashPPCall, pin *bashPPGoSourcePin) (*goSourceTaskArguments, map[*bashPPCell]bool) {
	var fn *bashPPFunc
	var ok bool
	if call.FuncLit != nil {
		// Preparing a literal's signature must not retain an extra callable
		// in the parent registry across Reset. The child instantiates it.
		fn, ok = &bashPPFunc{lit: call.FuncLit, scope: r.bashPPScope, typeArgs: r.bashPPTypeParamArgs}, true
	} else if pin != nil && pin.bound != nil {
		fn, ok = pin.bound, true
	} else if pin != nil {
		fn, ok = r.bashPPClosure(pin.handle)
	} else {
		fn, ok = r.bashPPLookupFunc(call)
	}
	if !ok || fn == nil {
		r.exit.fatal(fmt.Errorf("gosource: launched original function is unavailable"))
		return nil, nil
	}
	savedCells, savedChannels, savedInterfaces := r.bashPPCallCells, r.bashPPCallChannels, r.bashPPCallInterfaces
	defer func() {
		r.bashPPCallCells, r.bashPPCallChannels, r.bashPPCallInterfaces = savedCells, savedChannels, savedInterfaces
	}()
	var args []string
	var err error
	if call.ArgExprs != nil {
		args, ok, err = r.bashPPTypedCallArgs(call, fn)
	} else {
		args, ok = r.bashPPCallValues(call, fn)
	}
	if err != nil {
		if !r.bashPPPanicking() {
			r.exit.fatal(err)
		}
		return nil, nil
	}
	// A panic halting the frame launches nothing; a deferred call running
	// for the panic is an ordinary statement sequence, and a goroutine it
	// starts runs as Go runs it — the deferred calls run before the unwind.
	if !ok || r.bashPPPanicHalts() || r.exit.exiting || r.exit.err != nil {
		return nil, nil
	}
	for _, cell := range r.bashPPCallCells {
		if cell != nil {
			// Retain the existing session/task-group admission check when a
			// value travels outside the shell snapshot. Later operations still
			// validate every native handle and channel they actually use.
			r.goSourceCapturedHandleCell(cell)
			if r.exit.err != nil {
				return nil, nil
			}
		}
	}
	prepared := &goSourceTaskArguments{pin: pin, args: args, cells: r.bashPPCallCells, channels: r.bashPPCallChannels, interfaces: r.bashPPCallInterfaces}
	// Argument expressions can create new original closures. Their lexical
	// references must be carried too, without evaluating either operand again.
	body, params := bashPPGoSourceFuncBody(fn.lit, fn.decl)
	env := fn.scope
	if env == nil {
		env = r.bashPPScope
	}
	shared, ok := r.bashPPGoSourceCaptureSet(call, body, params, env)
	if !ok {
		return nil, nil
	}
	return prepared, shared
}

func (r *Runner) goSourceInvokeTaskArguments(ctx context.Context, call *syntax.BashPPCall, prepared *goSourceTaskArguments) {
	fn, ok := r.bashPPLookupFunc(call)
	if !ok {
		if r.exit.code == 0 {
			r.exit.fatal(fmt.Errorf("gosource: launched original function is unavailable"))
		}
		return
	}
	r.bashPPCallCells, r.bashPPCallChannels, r.bashPPCallInterfaces = prepared.cells, prepared.channels, prepared.interfaces
	r.bashPPInvoke(ctx, fn, prepared.args)
}
