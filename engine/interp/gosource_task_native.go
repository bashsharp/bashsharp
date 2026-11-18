// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

// Sprint: #243; Story: #674; Story-ID: 63073886bfce

import (
	"context"
	"fmt"
	"go/constant"
	"strconv"
	"strings"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// GoSource launches whose callee has no original function body.
//
// `go println(…)`, `go close(ch)`, `go fmt.Println(x)`, `go mu.Unlock()` and
// `go f(c)` where f holds a dependency's function value are all legal Go, and
// none of them names an original body the lexical capture analysis in
// gosource_task_capture.go could walk. That analysis is the right tool for a
// closure: a goroutine running an original body shares the variables the body
// captures. It is the wrong question for these callees, which capture nothing —
// their whole input is the operand list — and until now they were refused as
// "the launched callee does not resolve to an original function body".
//
// # The rule
//
// Go's `go` statement fixes its function value and every operand in the
// LAUNCHING goroutine and then runs the call in the new one. A launch here
// therefore evaluates exactly once, in the parent, before any task exists:
//
//   - the function value — a predeclared name, a dependency selector, a bound
//     method value, a native function handle, or a nil function value;
//   - the operands, in source order, with every side effect and every panic
//     an operand can raise happening here. A panic raised while evaluating an
//     operand launches nothing, exactly as in Go.
//
// What travels to the task is the VALUE each operand produced, never the
// expression that produced it. Re-reading the expression inside the task —
// even against the deep-copied snapshot — would run a computed operand twice
// or on a copy, which is a different program. The predeclared builtins carry
// their operands as retained cells bound under private names; a dependency
// call carries the bridge request the operands were rendered into.
//
// The child's scope copy is the GoSource task scope: no unrelated parent
// local is copied, because nothing here can name one. The carried original
// callable descriptors still keep their exact lexical references (see
// [Runner.bashPPGoSourceCaptureSet]), which is what lets a dependency call
// back into an original method — `go f(c)` where f is a reflected method
// value of a local type — from inside the task.
//
// # Panic controls
//
// A builtin that panics when it runs — `close` of a nil or closed channel,
// `panic(v)` itself, a nil function value — panics in the TASK, after the
// launch has returned to the parent, and is reported as that task's failure:
// the parent cannot recover it, as in Go. `recover` outside any deferred
// call reports nothing and the task completes.
//
// # What stays refused
//
// A dependency call whose operands carry an original closure as a callback
// (`go sort.Slice(s, func(i, j int) bool {…})`). The closure was registered
// by the parent over the parent's live scope chain, and the task would run it
// there while the parent keeps executing; that is a data race in the
// interpreter, not merely in the program. The sync/atomic, unsafe.String and
// runtime stack operators that the bridge answers over interpreter storage
// are refused too: their "operand" is the parent's own storage, and there is
// no launch-time value to carry. Both are reported, not approximated.

// goSourceNativeLaunch is a prepared launch of a bodiless callee.
type goSourceNativeLaunch struct {
	// original carries a computed original callee already resolved once.
	original *goSourceTaskArguments
	// shared is the capture set for the carried original callable
	// descriptors; non-nil so the snapshot is a GoSource task scope.
	shared map[*bashPPCell]bool
	// run performs the call in the task, on the child runner.
	run func(ctx context.Context, child *Runner)
}

// goSourcePrepareNativeLaunch reports whether the launched call is a bodiless
// callee this file owns and, if so, evaluates its operands now. A nil launch
// with handled set means the call was claimed and diagnosed.
//
// The callee classes are tried in the order [Runner.bashPPCall] dispatches
// them, so a launch resolves the same function a direct call would: a nil
// function value, then a dependency operation, then an original function
// (declined here; it has a body), then a computed dependency function value,
// then the predeclared builtins.
func (r *Runner) goSourcePrepareNativeLaunch(ctx context.Context, call *syntax.BashPPCall) (*goSourceNativeLaunch, bool) {
	if r == nil || !r.bashPPGoSource || call == nil || call.FuncLit != nil {
		return nil, false
	}
	if r.bashPPNilFuncCall(call) {
		return r.goSourceNilFuncLaunch(call), true
	}
	if r.bashPPBridgeHandles(call) {
		return r.goSourceDependencyLaunch(ctx, call), true
	}
	if call.CalleeExpr != nil {
		// Original method selection must remain wholly owned by the existing
		// capture path: even looking it up here evaluates its receiver.
		if method, ok := call.CalleeExpr.(*syntax.BashPPSelectorExpr); ok && method.MethodValue && !r.bashPPNativeExpr(method.X) {
			return nil, false
		}
		cell, err := r.goSourceValueCell(call.CalleeExpr)
		if err != nil || cell == nil {
			if err != nil && !r.bashPPPanicking() {
				r.exit.fatal(err)
			}
			return nil, true
		}
		if cell.vr.Kind == expand.String {
			if fn, ok := r.bashPPClosure(cell.vr.Str); ok && fn.native == nil {
				_, pin := r.goSourcePinTaskCallable(call, fn, cell.vr.Str)
				prepared, shared := r.goSourcePrepareTaskArguments(call, pin)
				if prepared == nil {
					return nil, true
				}
				return &goSourceNativeLaunch{shared: shared, original: prepared}, true
			}
		}
		value, err := r.bashPPBridgeCell(cell)
		if err != nil {
			r.exit.fatal(err)
			return nil, true
		}
		if value.Kind == "nil" {
			return r.goSourceNilFuncLaunch(call), true
		}
		if value.Kind != "handle" || !(value.Function || strings.HasPrefix(value.Type, "func(")) {
			return r.goSourceLaunchRefused(call, "computed callee is not an authenticated function value"), true
		}
		fn := &bashPPFunc{native: &value}
		if sig := bashPPComputedCalleeSignature(call.CalleeExpr, cell); sig != nil {
			fn.lit = &syntax.BashPPFuncLit{Params: sig.Params, Results: sig.Results}
		}
		return r.goSourceNativeFuncLaunch(call, fn), true
	}
	if channel, ok, handled := r.goSourceCloseBuiltinOperand(call, "launched"); handled {
		if !ok {
			return nil, true
		}
		return r.goSourceLaunch(call, func(ctx context.Context, child *Runner) {
			child.goSourceCloseChannelValue(channel)
		}), true
	}
	if name := bashPPPredeclaredCall(call); name != "" {
		if r.bashPPFuncs[name] != nil || (r.bashPPScope != nil && r.bashPPScope.lookup(name) != nil) {
			return nil, false
		}
		return r.goSourceBuiltinLaunch(call, name), true
	}
	return nil, false
}

// goSourceLaunch pairs a prepared run with the capture set of the carried
// original callables. It is the last step of every launch: nothing below is
// evaluated after it.
func (r *Runner) goSourceLaunch(call *syntax.BashPPCall, run func(context.Context, *Runner)) *goSourceNativeLaunch {
	shared, ok := r.bashPPGoSourceCaptureSet(call, nil, nil, nil)
	if !ok {
		return nil
	}
	return &goSourceNativeLaunch{shared: shared, run: run}
}

// goSourceLaunchRefused reports a launch this file cannot give Go's meaning.
func (r *Runner) goSourceLaunchRefused(call *syntax.BashPPCall, reason string) *goSourceNativeLaunch {
	if !r.bashPPPanicking() {
		r.exit.fatal(fmt.Errorf("%sgosource: unsupported task launch: %s", r.bashErrPrefix(call.Pos()), reason))
	}
	return nil
}

// goSourceNilFuncLaunch launches a call of a nil function value. The operands
// are still evaluated in the parent — Go evaluates them before the call
// faults — and the fault is raised in the task.
func (r *Runner) goSourceNilFuncLaunch(call *syntax.BashPPCall) *goSourceNativeLaunch {
	_, _, ok := r.goSourceCaptureLaunchedBuiltin(call, "nil function")
	if !ok || r.exit.code != 0 || r.exit.err != nil || r.bashPPPanicHalts() {
		return nil
	}
	return r.goSourceLaunch(call, func(ctx context.Context, child *Runner) {
		child.bashPPRaiseNilFuncCall()
	})
}

// goSourceDependencyLaunch launches an imported dependency operation: a
// package function, a method on a native receiver, or a function value held
// in a variable. The request is prepared in the parent exactly as a direct
// call prepares it, a method receiver is bound to its method value now, and
// the task issues the request on its own runner so any callback the
// dependency raises is served by the task that made the call.
func (r *Runner) goSourceDependencyLaunch(ctx context.Context, call *syntax.BashPPCall) *goSourceNativeLaunch {
	if name, ok := r.goSourceInterpreterStorageCall(call); ok {
		return r.goSourceLaunchRefused(call, name+" operates on interpreter-owned storage and has no launch-time operand value to carry")
	}
	req, err := r.bashPPEvalRequest()
	if err != nil {
		r.exit.fatal(err)
		return nil
	}
	q, err := r.bashPPPrepareNativeCall(ctx, call)
	if err != nil {
		if !r.bashPPPanicking() {
			r.exit.fatal(err)
		}
		return nil
	}
	if r.exit.code != 0 || r.exit.err != nil || r.bashPPPanicHalts() {
		return nil
	}
	if q.Receiver != nil && q.Selector != "" {
		bound, err := r.bashPPBindNativeMethod(ctx, *q.Receiver, q.Selector)
		if err != nil {
			r.exit.fatal(err)
			return nil
		}
		q.Receiver, q.Selector = &bound, ""
	}
	r.bashPPReflectValueReceiver(req, call, &q)
	if goSourceBridgeValuesCarryClosure(q.Args) {
		return r.goSourceLaunchRefused(call, "an original closure passed to a launched dependency call would run over the parent's live scope from another task")
	}
	return r.goSourceLaunch(call, func(ctx context.Context, child *Runner) {
		req, err := child.bashPPEvalRequest()
		if err != nil {
			child.exit.fatal(err)
			return
		}
		if !child.goSourceNativeSleepBoundary(ctx, req, q) {
			return
		}
		if _, err := child.bashPPNativeRequest(ctx, req, q); err != nil && !child.bashPPPanicking() {
			child.exit.fatal(err)
		}
	})
}

// goSourceInterpreterStorageCall names a dependency selector the bridge
// answers over the interpreter's own storage rather than by request.
func (r *Runner) goSourceInterpreterStorageCall(call *syntax.BashPPCall) (string, bool) {
	if name, ok := r.goSourceAtomicSelector(call); ok {
		if _, _, atomic := goSourceAtomicOperation(name); atomic {
			return "sync/atomic " + name, true
		}
	}
	if len(call.Fun) == 2 && call.Fun[1].Value == "String" && r.bashPPImports[call.Fun[0].Value] == "unsafe" {
		return "unsafe.String", true
	}
	if name, ok := r.goSourceStackSelector(call); ok {
		return name, true
	}
	return "", false
}

// goSourceBridgeValuesCarryClosure reports an operand that is, or contains,
// an original closure registered as a dependency callback.
func goSourceBridgeValuesCarryClosure(values []bashPPBridgeValue) bool {
	for _, v := range values {
		if v.Kind == "callback" {
			return true
		}
		if goSourceBridgeValuesCarryClosure(v.Elements) {
			return true
		}
		for _, field := range v.Fields {
			if goSourceBridgeValuesCarryClosure([]bashPPBridgeValue{field}) {
				return true
			}
		}
		for _, entry := range v.Entries {
			if goSourceBridgeValuesCarryClosure([]bashPPBridgeValue{entry.Key, entry.Value}) {
				return true
			}
		}
	}
	return false
}

// goSourceNativeFuncLaunch launches a computed callee that evaluated to a
// dependency's function value — `go m.Func.Interface().(func(M))(v)`,
// `go fs[i]()`. The callee was evaluated once, above; the operands are bound
// to its asserted signature here and travel as value cells outside the
// snapshot, exactly as an original function's do.
func (r *Runner) goSourceNativeFuncLaunch(call *syntax.BashPPCall, fn *bashPPFunc) *goSourceNativeLaunch {
	savedCells, savedChannels, savedInterfaces := r.bashPPCallCells, r.bashPPCallChannels, r.bashPPCallInterfaces
	defer func() {
		r.bashPPCallCells, r.bashPPCallChannels, r.bashPPCallInterfaces = savedCells, savedChannels, savedInterfaces
	}()
	args, ok := r.bashPPCallValues(call, fn)
	if !ok || r.exit.code != 0 || r.exit.err != nil || r.bashPPPanicHalts() {
		return nil
	}
	for _, cell := range r.bashPPCallCells {
		if cell != nil {
			if value, err := r.bashPPBridgeCell(cell); err == nil && goSourceBridgeValuesCarryClosure([]bashPPBridgeValue{value}) {
				return r.goSourceLaunchRefused(call, "an original closure passed to a launched dependency call would run over the parent's live scope from another task")
			}
			r.goSourceCapturedHandleCell(cell)
			if r.exit.err != nil {
				return nil
			}
		}
	}
	cells, channels, interfaces := r.bashPPCallCells, r.bashPPCallChannels, r.bashPPCallInterfaces
	return r.goSourceLaunch(call, func(ctx context.Context, child *Runner) {
		child.bashPPCallCells, child.bashPPCallChannels, child.bashPPCallInterfaces = cells, channels, interfaces
		child.bashPPInvoke(ctx, fn, args)
	})
}

// goSourceBuiltinLaunch launches a predeclared builtin. Only the builtins Go
// permits in statement context can be launched; the value-producing ones are
// a compile error in Go (`go discards result of len(x)`) and are reported as
// such rather than run for their side effects.
func (r *Runner) goSourceBuiltinLaunch(call *syntax.BashPPCall, name string) *goSourceNativeLaunch {
	switch name {
	case "clear", "copy", "delete", "panic", "print", "println", "recover":
	default:
		return r.goSourceLaunchRefused(call, "go discards result of "+name+"(…)")
	}
	if name == "panic" {
		return r.goSourcePanicLaunch(call)
	}
	if name == "recover" {
		return r.goSourceLaunch(call, func(ctx context.Context, child *Runner) {
			// Not called directly by a deferred function: recover returns
			// nil, and its "nothing to recover" status is this call's answer,
			// not a task failure.
			child.bashPPPredeclared(name, call, child.bashPPCallArgValues(call))
			if child.exit.errexitExempt {
				child.exit = exitStatus{}
			}
		})
	}
	captured, cells, ok := r.goSourceCaptureLaunchedBuiltin(call, name)
	if !ok {
		return nil
	}
	return r.goSourceLaunch(call, func(ctx context.Context, child *Runner) {
		// The retained operand cells are bound in a scope of their own on
		// the task: they were produced by the parent's statement, and the
		// task scope copy carries no parent local that could name them.
		leave := child.bashPPPushScope()
		defer leave()
		for argName, cell := range cells {
			child.bashPPScope.put(argName, cell)
		}
		if bashPPValueBuiltin(name) {
			child.bashPPRunValueBuiltin(name, captured)
			return
		}
		child.bashPPPredeclared(name, captured, child.bashPPCallArgValues(captured))
	})
}

// goSourcePanicLaunch launches `go panic(v)`. The value and its report text
// are evaluated here through the same rule the direct call uses, so a
// dependency error, a program-declared type or a scalar panics in the task
// with the value the parent computed; the raise itself, and the trace it
// records, are the task's.
func (r *Runner) goSourcePanicLaunch(call *syntax.BashPPCall) *goSourceNativeLaunch {
	if len(call.Args) != 1 || call.Ellipsis.IsValid() {
		r.errf("panic: takes exactly one argument\n")
		r.exit = exitStatus{code: 2}
		return nil
	}
	captured, cells, ok := r.goSourceCaptureLaunchedBuiltin(call, "panic")
	if !ok {
		return nil
	}
	// The direct panic conversion may inspect its operand along multiple type
	// paths. Bind the already evaluated value, never the source expression.
	leave := r.bashPPPushScope()
	for name, cell := range cells {
		r.bashPPScope.put(name, cell)
	}
	args := r.bashPPCallArgValues(captured)
	var value any
	var text string
	if boxed, nativeText, native := r.goSourcePanicNativeValue(captured.ArgExprs[0]); native {
		value, text, ok = boxed, nativeText, boxed != nil
	} else {
		value, text, ok = r.bashPPPanicOperand(captured, args[0])
	}
	leave()
	if !ok || r.exit.code != 0 || r.exit.err != nil || r.bashPPPanicHalts() {
		return nil
	}
	return r.goSourceLaunch(call, func(ctx context.Context, child *Runner) {
		child.bashPPPanicTrace(call)
		child.bashPPRaiseValue(text, value)
	})
}

// goSourceCaptureLaunchedBuiltin evaluates a builtin's operands where the go
// statement runs and returns the call the task will run in their place.
//
// A print operand is only ever printed, so its text is fixed here in Go's
// print form and carried as a string literal the direct print path renders
// verbatim — the same rule [Runner.goSourceCaptureDeferredValueBuiltin]
// applies to a deferred print. Every other operand is retained as the value
// cell it produced, under a private name the captured call refers to, so the
// task sees the operand's type as well as its value: `go delete(m, k)`
// deletes from the map the parent holds, `go copy(dst, src)` writes the
// parent's backing array. The cells are copies the parent never sees again; reference
// operands keep their map/slice/pointee identity as Go's assignment does.
func (r *Runner) goSourceCaptureLaunchedBuiltin(call *syntax.BashPPCall, name string) (*syntax.BashPPCall, map[string]*bashPPCell, bool) {
	if len(call.Args) != len(call.ArgExprs) {
		r.errf("gosource: launched %s requires positioned operands\n", name)
		r.exit = exitStatus{code: 2}
		return nil, nil, false
	}
	captured := *call
	captured.Args = append([]*syntax.Word(nil), call.Args...)
	captured.ArgExprs = make([]syntax.BashPPExpr, len(call.Args))
	cells := make(map[string]*bashPPCell)
	printing := name == "print" || name == "println"
	printText := func(i int, text string) {
		captured.Args[i] = &syntax.Word{Parts: []syntax.WordPart{&syntax.SglQuoted{
			Left: call.Args[i].Pos(), Right: call.Args[i].End(), Value: text,
		}}}
		captured.ArgExprs[i] = &syntax.BashPPBasicLit{Kind: "STRING", Value: &syntax.Lit{
			Value: strconv.Quote(text), ValuePos: call.Args[i].Pos(), ValueEnd: call.Args[i].End(),
		}}
	}
	for i, expr := range call.ArgExprs {
		if expr == nil {
			r.errf("%sgosource: launched %s operand has no expression\n", r.bashErrPrefix(call.Args[i].Pos()), name)
			r.exit = exitStatus{code: 2}
			return nil, nil, false
		}
		if printing && r.goSourcePrintReferenceOperand(expr) {
			text, err := r.goSourcePrintReference(expr)
			if err != nil {
				if !r.bashPPPanicking() {
					r.exit.fatal(err)
				}
				return nil, nil, false
			}
			printText(i, text)
			continue
		}
		cell, err := r.goSourceValueCell(expr)
		if err != nil {
			if !r.bashPPPanicking() {
				r.exit.fatal(err)
			}
			return nil, nil, false
		}
		if r.bashPPPanicHalts() || r.exit.code != 0 {
			// An operand panicked or failed while being evaluated: the
			// launch never happens.
			return nil, nil, false
		}
		if cell == nil {
			r.errf("%sgosource: launched %s operand has no value\n", r.bashErrPrefix(call.Args[i].Pos()), name)
			r.exit = exitStatus{code: 2}
			return nil, nil, false
		}
		if printing && cell.vr.Kind != expand.Object && cell.channel == nil && cell.interfaceValue == nil && !cell.pointer {
			scalar := r.bashPPScalarFromCell(cell)
			if (scalar.value == nil || scalar.value.Kind() == constant.Unknown) && !scalar.hasNonFinite {
				r.errf("%sgosource: launched %s scalar operand has no value\n", r.bashErrPrefix(call.Args[i].Pos()), name)
				r.exit = exitStatus{code: 2}
				return nil, nil, false
			}
			printText(i, r.goSourcePrintScalar(scalar))
			continue
		}
		argName := fmt.Sprintf("bashPPLaunchedArg_%d_%d", uint(call.Pos().Offset()), i)
		cells[argName] = bashPPCopyAssignmentCell(cell)
		captured.Args[i] = &syntax.Word{Parts: []syntax.WordPart{&syntax.Lit{
			Value: argName, ValuePos: call.Args[i].Pos(), ValueEnd: call.Args[i].End(),
		}}}
		if printing {
			// The direct print path reads a retained reference operand
			// from its word; an expression here would re-evaluate it.
			captured.ArgExprs[i] = nil
			continue
		}
		captured.ArgExprs[i] = &syntax.BashPPIdent{Name: &syntax.Lit{
			Value: argName, ValuePos: call.Args[i].Pos(), ValueEnd: call.Args[i].End(),
		}}
	}
	return &captured, cells, true
}
