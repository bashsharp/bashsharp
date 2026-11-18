package interp

import (
	"context"
	"fmt"
	"strconv"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/polyglot"
)

// Sprint 221 story #577 (B11/B12): the interpreter side of Rust opaque handles
// and shell callbacks. A handle result is an Object cell holding the
// polyglot.Handle, a handle parameter reads that Object back from the call's
// argument cell, and `alias.release(h)` is the module's synthetic release
// export. A callback parameter receives the name of a Bash# function or a
// closure value; the module resolves it through bashPPForeignCallbacks and
// invokes it, on the goroutine that serves the worker's protocol stream,
// while the foreign call stays parked in bashPPForeignExchange.

// bashPPForeignCallbacks wires a foreign module's shell callbacks to this
// runner: named callables resolve to Bash# functions and closures, and island
// output produced before a callback reaches the shell's streams first.
func (r *Runner) bashPPForeignCallbacks() polyglot.Callbacks {
	return polyglot.Callbacks{
		Resolve: func(name string) (polyglot.Callback, bool) {
			fn, ok := r.bashPPCallbackTarget(name)
			if !ok {
				return polyglot.Callback{}, false
			}
			return polyglot.Callback{Name: name, Invoke: func(ctx context.Context, args []any) (any, error) {
				return r.bashPPForeignCallback(ctx, name, fn, args)
			}}, true
		},
		Output: func(stdout, stderr string) {
			if stdout != "" {
				fmt.Fprint(r.stdout, stdout)
			}
			if stderr != "" {
				fmt.Fprint(r.stderr, stderr)
			}
		},
	}
}

// bashPPCallbackTarget resolves what a script passed for a callback
// parameter: a closure value (its handle text), a variable holding one, or
// the name of a Bash# function.
func (r *Runner) bashPPCallbackTarget(name string) (*bashPPFunc, bool) {
	if fn, ok := r.bashPPClosure(name); ok {
		return fn, true
	}
	if fn, ok := r.bashPPScopedClosureCallee(name); ok {
		return fn, true
	}
	if fn, ok := r.bashPPFuncs[name]; ok {
		return fn, true
	}
	if vr := r.lookupVar(name); vr.Kind == expand.String {
		if fn, ok := r.bashPPClosure(vr.Str); ok {
			return fn, true
		}
	}
	return nil, false
}

// bashPPForeignCallback runs one Bash# function for a foreign worker. The
// caller's in-flight call state is restored afterwards, exactly as the Go
// native bridge's callbacks do; a panic in the callback and a failing status
// are the worker's error, never a crash of the shell.
func (r *Runner) bashPPForeignCallback(ctx context.Context, name string, fn *bashPPFunc, args []any) (result any, err error) {
	defer func() {
		if failure := recover(); failure != nil {
			result, err = nil, fmt.Errorf("bash++: shell callback %s: interpreter failure: %v", name, failure)
		}
	}()
	params := bashppParams(fn.params())
	if len(args) != len(params) && !bashppVariadic(fn.params()) {
		return nil, fmt.Errorf("bash++: shell callback %s expects %d arguments, got %d", name, len(params), len(args))
	}
	savedResults, savedCalls := r.bashPPResultCells, r.bashPPCallCells
	savedChannels, savedInterfaces := r.bashPPCallChannels, r.bashPPCallInterfaces
	savedExit, savedPanic, savedCtx := r.exit, r.bashPPPanic, r.ectx
	failure := r.bashPPShortFailureSeq
	defer func() {
		r.bashPPResultCells, r.bashPPCallCells = savedResults, savedCalls
		r.bashPPCallChannels, r.bashPPCallInterfaces = savedChannels, savedInterfaces
		r.exit, r.bashPPPanic = savedExit, savedPanic
		r.ectx = savedCtx
	}()
	r.ectx = ctx
	r.bashPPCallChannels, r.bashPPCallInterfaces = nil, nil
	texts := make([]string, len(args))
	cells := make([]*bashPPCell, len(args))
	for i, arg := range args {
		texts[i] = foreignResult(arg)
		switch arg.(type) {
		case *polyglot.Handle, map[string]any, []any:
			cells[i] = &bashPPCell{vr: expand.NewObject(arg)}
		}
	}
	r.bashPPCallCells = cells
	results := r.bashPPInvoke(ctx, fn, texts)
	if r.bashPPPanicking() && !r.exit.exiting {
		payload := r.bashPPPanic.value()
		r.bashPPPanic, r.exit = savedPanic, savedExit
		return nil, fmt.Errorf("bash++: shell callback %s panicked: %s", name, payload)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.exit.err != nil {
		return nil, r.exit.err
	}
	if r.exit.exiting || r.exit.fatalExit || r.exit.code != 0 || r.bashPPShortFailureSeq != failure {
		code := r.exit.code
		r.exit = savedExit
		return nil, &polyglot.CallbackFailure{Message: fmt.Sprintf("bash++: shell callback %s failed (status %d)", name, code), Status: int(code)}
	}
	resultTypes := bashppResultTypes(fn.results())
	values := make([]any, len(results))
	for i, text := range results {
		if i < len(r.bashPPResultCells) && r.bashPPResultCells[i] != nil && r.bashPPResultCells[i].vr.Kind == expand.Object {
			values[i] = r.bashPPResultCells[i].vr.Obj
			continue
		}
		typ := ""
		if i < len(resultTypes) {
			typ = resultTypes[i]
		}
		values[i] = bashPPCallbackValue(text, typ)
	}
	switch len(values) {
	case 0:
		return nil, nil
	case 1:
		return values[0], nil
	}
	return values, nil
}

// bashPPCallbackValue gives a callback's text result the boundary kind its
// declared type names, so a Rust `call_as::<i64>` sees a number.
func bashPPCallbackValue(text, typ string) any {
	switch typ {
	case "int", "int64", "int32", "uint", "uint64":
		if value, err := strconv.ParseInt(text, 10, 64); err == nil {
			return value
		}
	case "float64", "float32":
		if value, err := strconv.ParseFloat(text, 64); err == nil {
			return value
		}
	case "bool":
		if value, err := strconv.ParseBool(text); err == nil {
			return value
		}
	}
	return text
}
