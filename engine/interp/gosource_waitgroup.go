// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

// Sprint: #118; Story: #54; Story-ID: c3a60493cde9
//
// `wg.Go(f)` is the one bounded asynchronous callback surface a Go original
// may name. See gosource_waitgroup.md for the equivalence argument against the
// pinned Go 1.27 sync/waitgroup.go; the short version is that the dependency
// never receives f. The interpreter runs the original body in a task of its
// own and drives the *native* counter with Add/Done around it, so the object
// being waited on stays the dependency's one WaitGroup.

import (
	"context"
	"errors"
	"fmt"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// goSourceWaitGroupGo answers a `wg.Go(f)` statement, reporting whether it
// claimed the call. It claims nothing it cannot answer exactly: an unclaimed
// call falls through to the ordinary native path and keeps whatever diagnostic
// that path already produces.
func (r *Runner) goSourceWaitGroupGo(ctx context.Context, call *syntax.BashPPCall) bool {
	if !r.bashPPGoSource || call == nil || len(call.ArgExprs) != 1 || call.Ellipsis.IsValid() {
		return false
	}
	base, ok := goSourceWaitGroupSelector(call)
	if !ok || !(r.bashPPNativeExpr(base) || r.bashPPNativePointerExpr(base)) {
		return false
	}
	// The argument shape is checked before the receiver is evaluated, so an
	// unclaimed call has not touched the dependency at all.
	body, ok := r.goSourceWaitGroupBody(call.ArgExprs[0])
	if !ok {
		return false
	}
	receiver, err := r.bashPPNativeReceiver(base)
	if err != nil {
		// Not this operation's diagnostic to own; the native path re-reports it.
		return false
	}
	// An original pointer to the dependency's WaitGroup names the same native
	// object; the methods run against the handle's own storage.
	if receiver.Kind == "pointer" && len(receiver.Elements) == 1 && receiver.Elements[0].Kind == "handle" {
		receiver = receiver.Elements[0]
	}
	if !goSourceWaitGroupHandle(receiver) {
		return false
	}
	if !r.bashPPFileRun {
		r.errf("bash++: sync.WaitGroup.Go requires an owning File Run\n")
		r.exit.code = 2
		return true
	}
	r.goSourceWaitGroupLaunch(ctx, receiver, body, call)
	return true
}

// goSourceWaitGroupSelector returns the receiver expression of a `x.Go(f)`
// call, in either spelling the parser produces: a computed callee, or the
// literal selector chain an ordinary `wg.Go(…)` statement carries. It reports
// false for every other callee, including a bare `Go(f)`.
func goSourceWaitGroupSelector(call *syntax.BashPPCall) (syntax.BashPPExpr, bool) {
	if call.CalleeExpr != nil {
		selector, ok := call.CalleeExpr.(*syntax.BashPPSelectorExpr)
		if !ok || selector.Sel == nil || selector.Sel.Value != "Go" {
			return nil, false
		}
		return selector.X, true
	}
	if len(call.Fun) < 2 || call.Fun[len(call.Fun)-1].Value != "Go" {
		return nil, false
	}
	var base syntax.BashPPExpr = &syntax.BashPPIdent{Name: call.Fun[0]}
	for _, part := range call.Fun[1 : len(call.Fun)-1] {
		base = &syntax.BashPPSelectorExpr{X: base, Sel: part}
	}
	return base, true
}

// goSourceWaitGroupHandle authenticates the receiver as a live WaitGroup owned
// by the dependency session. The native type is the one the dependency itself
// derived from the object's reflect.Type, so a same-named method on any other
// type — including an original local type — is never claimed here.
func goSourceWaitGroupHandle(value bashPPBridgeValue) bool {
	if value.Kind != "handle" || value.Session == "" {
		return false
	}
	name := value.NativeType
	if name == "" {
		name = value.Type
	}
	return name == "sync.WaitGroup" || name == "*sync.WaitGroup"
}

// goSourceWaitGroupBody reports the original `func()` an argument names,
// without evaluating it. Only shapes whose evaluation has no observable effect
// are accepted, which is what lets the closure be built once inside the task —
// exactly as `go func() { … }()` builds its own — rather than in the launcher.
func (r *Runner) goSourceWaitGroupBody(expr syntax.BashPPExpr) (syntax.BashPPExpr, bool) {
	switch x := expr.(type) {
	case *syntax.BashPPParenExpr:
		return r.goSourceWaitGroupBody(x.X)
	case *syntax.BashPPFuncLit:
		if len(x.Params) == 0 && len(x.Results) == 0 {
			return x, true
		}
	case *syntax.BashPPIdent:
		if fn, ok := r.goSourceWaitGroupNamed(x.Name.Value); ok &&
			len(bashppParams(fn.params())) == 0 && bashppResultCount(fn.results()) == 0 {
			return x, true
		}
	}
	return nil, false
}

// goSourceWaitGroupNamed resolves a name to the interpreted function it is
// bound to, using only lookups. A native function value is not an original
// body, so it is not one this operation may spawn.
func (r *Runner) goSourceWaitGroupNamed(name string) (*bashPPFunc, bool) {
	if r.bashPPScope != nil {
		if cell := r.bashPPScope.lookup(name); cell != nil {
			fn, ok := r.bashPPClosure(cell.viewVar().Str)
			return fn, ok && fn.native == nil
		}
	}
	if fn := r.bashPPFuncs[name]; fn != nil {
		return fn, fn.native == nil
	}
	if vr := r.lookupVar(name); vr.Set {
		fn, ok := r.bashPPClosure(vr.Str)
		return fn, ok && fn.native == nil
	}
	return nil, false
}

// goSourceWaitGroupLaunch is Go 1.27's `Add(1); go func(){ defer Done(); f() }()`
// with an interpreted task standing in for the goroutine. It mirrors
// [Runner.bashPPGo]'s lifetime — snapshot, launch handshake, private EXIT trap,
// group failure reporting — because a WaitGroup task is a task like any other;
// what it adds is the native counter either side of the body.
func (r *Runner) goSourceWaitGroupLaunch(ctx context.Context, wg bashPPBridgeValue, body syntax.BashPPExpr, call *syntax.BashPPCall) {
	// `wg.Go(f)` evaluates f in the CALLING goroutine, exactly as `go f()`
	// evaluates its function value there. So the spawn call is built once, in
	// the parent, and handed to the same helper `go` uses: one lexical capture
	// analysis, one resolution of the callee, one pin. Nothing about the body
	// is compiled, forwarded or credited to the dependency — the pin is a
	// handle into this interpreter's own closure registry, and the body is
	// interpreted in the task.
	spawn := r.goSourceWaitGroupSpawnCall(body, call)
	shared, pin := r.bashPPGoSourceTaskCapture(spawn)
	if r.exit.code != 0 || r.exit.err != nil {
		// A refused capture analysis is the launch's own diagnostic. Nothing
		// has been added to the WaitGroup and no task has been registered.
		return
	}
	c := r.bashPPConcurrency(ctx)
	state, ok := c.add()
	if !ok {
		// A prior child failure owns the eventual File status. Nothing has been
		// added to the WaitGroup yet, so nothing is left owing on it either.
		return
	}
	ordinal := state.ordinal
	child, err := r.bashPPTaskSnapshot(ordinal, shared)
	if err != nil {
		if child != nil {
			child.closeBashPPTaskResources()
		}
		c.done(ordinal, &bashPPTaskFailure{ordinal: ordinal, code: 2, text: fmt.Sprintf("task snapshot: %v", err)})
		return
	}
	child.bashPPTaskState = state
	// Add happens in the launcher and before the task exists, so `Go` still
	// happens-before any later `Wait`, and the counter can never be observed
	// at zero between the launch and the body starting.
	if err := r.goSourceWaitGroupCount(ctx, wg, "Add", bashPPBridgeValue{Kind: "int", Text: "1"}); err != nil {
		child.closeBashPPTaskResources()
		if errors.Is(err, errBashPPNativeExited) || r.bashPPPanicking() {
			c.done(ordinal, nil)
			return
		}
		c.done(ordinal, &bashPPTaskFailure{ordinal: ordinal, code: 2, text: fmt.Sprintf("sync.WaitGroup.Add: %v", err)})
		return
	}
	if c.ctx.Err() != nil {
		// The group is already unwinding. Release the count taken above so a
		// launcher blocked in Wait is not stranded behind a task never run.
		r.goSourceWaitGroupCount(ctx, wg, "Done")
		child.closeBashPPTaskResources()
		c.done(ordinal, nil)
		return
	}
	go func() {
		var failure *bashPPTaskFailure
		// Done is owed from here on, and is released for every exit except a
		// panicking one — Go 1.27 re-panics without decrementing.
		owed := true
		defer func() {
			if x := recover(); x != nil {
				owed = false
				failure = &bashPPTaskFailure{ordinal: ordinal, code: 2, text: fmt.Sprintf("panic: %v", x)}
			}
			// A task is a terminal shell lifetime; its private EXIT snapshot
			// runs even when group cancellation has already fired.
			func() {
				defer func() {
					if x := recover(); x != nil && failure == nil {
						failure = &bashPPTaskFailure{ordinal: ordinal, code: 2, text: fmt.Sprintf("EXIT trap panic: %v", x)}
					}
				}()
				child.trapCallback(context.WithoutCancel(c.ctx), child.trapCallbacks["EXIT"], "exit")
			}()
			if owed {
				if err := child.goSourceWaitGroupCount(context.WithoutCancel(c.ctx), wg, "Done"); err != nil && failure == nil {
					failure = &bashPPTaskFailure{ordinal: ordinal, code: 2, text: fmt.Sprintf("sync.WaitGroup.Done: %v", err)}
				}
			}
			child.closeBashPPTaskResources()
			c.done(ordinal, failure)
		}()
		if c.ctx.Err() != nil {
			return
		}
		// The callee was resolved once, in the parent; the child runs that
		// exact function through its own cloned registry.
		child.bashPPGoSourcePin = pin
		child.goSourceWaitGroupInvoke(c.ctx, spawn)
		if child.bashPPPanicking() {
			// The body left panicking and nothing recovered it. Go's WaitGroup
			// re-panics rather than releasing the counter, so neither does this.
			owed = false
		}
		code := child.exit.code
		canceled := child.bashPPTaskCanceled || errors.Is(child.exit.err, context.Canceled) || errors.Is(child.exit.err, context.DeadlineExceeded)
		if canceled {
			return
		}
		if code != 0 && child.trapCallbacks["ERR"] != "" {
			child.trapCallback(c.ctx, child.trapCallbacks["ERR"], "error")
		}
		if code != 0 {
			failure = &bashPPTaskFailure{ordinal: ordinal, code: code, text: fmt.Sprintf("exit status %d", code)}
		}
	}()
	// The same deterministic launch handshake `go` uses; see [Runner.bashPPGo].
	<-state.ready
}

// goSourceWaitGroupSpawnCall is the launched body written as the call the task
// runs, in the spelling the shared task helpers already understand.
//
// A literal goes in FuncLit, which is the same node `go func(){…}()` presents,
// so the closure is built against the task's own scope and the capture set
// decides what that scope shares. A name goes in CalleeExpr, which is what
// makes it resolvable — and therefore pinnable — in the parent.
func (r *Runner) goSourceWaitGroupSpawnCall(body syntax.BashPPExpr, call *syntax.BashPPCall) *syntax.BashPPCall {
	spawn := &syntax.BashPPCall{Lparen: call.Lparen, Rparen: call.Rparen}
	if lit, ok := body.(*syntax.BashPPFuncLit); ok {
		spawn.FuncLit = lit
		return spawn
	}
	spawn.CalleeExpr = body
	return spawn
}

// goSourceWaitGroupInvoke resolves the spawn call to one closure and runs it
// once in this task. The body is interpreted here; nothing about it is
// forwarded to the dependency.
func (r *Runner) goSourceWaitGroupInvoke(ctx context.Context, spawn *syntax.BashPPCall) {
	fn, ok := r.bashPPLookupFunc(spawn)
	if !ok {
		if r.exit.code == 0 && r.exit.err == nil {
			pos := spawn.Lparen
			if spawn.CalleeExpr != nil {
				pos = spawn.CalleeExpr.Pos()
			} else if spawn.FuncLit != nil {
				pos = spawn.FuncLit.Pos()
			}
			r.exit.fatal(fmt.Errorf("%sgosource: sync.WaitGroup.Go argument is not an original function", r.bashErrPrefix(pos)))
		}
		return
	}
	r.bashPPInvoke(ctx, fn, nil)
}

// goSourceWaitGroupCount runs one native counter operation against the exact
// handle the receiver named. It carries no callback and no interpreter-owned
// reference, so it is an ordinary one-shot dependency request.
func (r *Runner) goSourceWaitGroupCount(ctx context.Context, wg bashPPBridgeValue, selector string, args ...bashPPBridgeValue) error {
	req, err := r.bashPPEvalRequest()
	if err != nil {
		return err
	}
	receiver := wg
	_, err = r.bashPPNativeRequest(ctx, req, bashPPBridgeRequest{Op: "call", Selector: selector, Receiver: &receiver, Args: args})
	return err
}
