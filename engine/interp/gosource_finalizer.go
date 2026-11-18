package interp

// Sprint: #248; Story: #424; Story-ID: ffabc6c1c44a

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"time"
	"weak"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// RETAINED FINALIZERS ON INTERPRETER REACHABILITY.
//
// runtime.SetFinalizer over an interpreter pointer used to cross the native
// bridge, where the dependency could only finalize a mirror whose lifetime
// has nothing to do with the original (Story 424). The finalizer is now kept
// on the interpreter's own allocation instead: an interpreter pointer names a
// target cell, and that cell is an ordinary object of this process, reachable
// exactly as long as some binding, aggregate, closure or pointer of the
// program reaches it (see gosource_liveness.go for when bindings stop doing
// so). A host finalizer on the cell therefore fires only once the program can
// no longer reach the object — never early — and, as in Go, at most once.
//
// Delivery follows the Go contract: the original finalizer function runs on
// its own goroutine, with the original pointer (the same cell, so identity
// and resurrection are exact), sequentially for the finalizers found ready.
// The host finalizer only queues the object; interpreted code runs when the
// program next calls runtime.GC, which also makes that collection complete
// before it returns. Go permits a finalizer to run at any time after the
// object becomes unreachable, or not at all before the program exits.
//
// Shapes this cannot represent faithfully keep the prompt refusal the bridge
// gave before: an interior or zero-sized pointer, a dependency pointer, and a
// finalizer whose signature does not take the object's pointer type.

const goSourceFinalizerRefusal = "gosource: original callback signature requires value-semantics parameters and supported results"

type goSourceFinalizer struct {
	fn   *bashPPFunc
	elem syntax.BashPPTypeExpr
}

type goSourceFinalizerRun struct {
	fin    *goSourceFinalizer
	target *bashPPCell
}

// goSourceFinalizers is the finalizer table of one program. It hangs off the
// task group, which every goroutine of the program shares.
type goSourceFinalizers struct {
	mu      sync.Mutex
	armed   map[weak.Pointer[bashPPCell]]*goSourceFinalizer
	ready   []goSourceFinalizerRun
	running chan struct{}
}

func (r *Runner) goSourceFinalizerTable(ctx context.Context) *goSourceFinalizers {
	c := r.bashPPConcurrency(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.finalizers == nil {
		c.finalizers = &goSourceFinalizers{armed: map[weak.Pointer[bashPPCell]]*goSourceFinalizer{}}
	}
	return c.finalizers
}

// goSourceFinalizerCall answers runtime.SetFinalizer over an interpreter
// pointer, and makes runtime.GC deliver the finalizers the collection found.
// runtime.GC itself still continues to the dependency afterwards.
func (r *Runner) goSourceFinalizerCall(ctx context.Context, call *syntax.BashPPCall) ([]bashPPBridgeValue, bool, error) {
	if !r.bashPPGoSource || call == nil || call.Ellipsis.IsValid() {
		return nil, false, nil
	}
	name, ok := r.goSourceStackSelector(call)
	if !ok {
		return nil, false, nil
	}
	switch {
	case name == "runtime.GC" && len(call.ArgExprs) == 0:
		if err := r.goSourceCollectFinalizers(ctx, call); err != nil {
			return nil, true, err
		}
		return nil, false, nil
	case name == "runtime.SetFinalizer" && len(call.ArgExprs) == 2 && len(call.Args) == 2:
		return r.goSourceSetFinalizer(ctx, call.ArgExprs[0], call.ArgExprs[1])
	}
	return nil, false, nil
}

func (r *Runner) goSourceSetFinalizer(ctx context.Context, objExpr, fnExpr syntax.BashPPExpr) ([]bashPPBridgeValue, bool, error) {
	// A dependency's own pointer stays with the dependency's runtime.
	if r.bashPPNativeExpr(objExpr) {
		return nil, false, nil
	}
	lit := goSourceFinalizerLit(fnExpr)
	clear := goSourceNilLiteral(fnExpr)
	if lit == nil && !clear && !r.goSourceFinalizerCallable(fnExpr) {
		return nil, false, nil
	}
	ptr, err := r.bashPPPointerExprValue(objExpr)
	if err != nil {
		// An operand that is not an interpreter pointer — a dependency
		// pointer, or a pointer held in an interface — is a shape this
		// representation does not serve; a runtime fault propagates.
		var native *bashPPNativePointerValueError
		if errors.As(err, &native) || strings.HasPrefix(err.Error(), "BASHPP-EPOINTER-TARGET") {
			return nil, true, errors.New(goSourceFinalizerRefusal)
		}
		return nil, true, err
	}
	if ptr == nil {
		r.goSourceFinalizerThrow("runtime.SetFinalizer: first argument is nil")
		return nil, true, errBashPPScalarInterrupted
	}
	if ptr.target == nil || len(ptr.path) > 0 || ptr.storageAddress != nil || ptr.unsafeSource() != nil || ptr.unsafeView() != nil {
		return nil, true, errors.New(goSourceFinalizerRefusal)
	}
	table := r.goSourceFinalizerTable(ctx)
	key := weak.Make(ptr.target)
	if clear {
		table.mu.Lock()
		delete(table.armed, key)
		table.mu.Unlock()
		runtime.SetFinalizer(ptr.target, nil)
		return nil, true, nil
	}
	var fn *bashPPFunc
	if recv, method, ok := r.goSourceFinalizerMethodExpr(fnExpr); ok {
		// A method expression captures nothing; its forwarding literal is
		// bound like a finalizer literal, outside the closure registry.
		if lit, err = r.goSourceMethodExprLit(recv, method); err != nil {
			return nil, true, err
		}
	}
	if lit != nil {
		fn = r.goSourceFinalizerClosure(lit)
	} else {
		cell, handled, err := r.goSourceCallableCell(fnExpr)
		if err != nil {
			return nil, true, err
		}
		if !handled || cell == nil {
			return nil, true, errors.New(goSourceFinalizerRefusal)
		}
		var ok bool
		if fn, ok = r.bashPPClosure(cell.vr.Str); !ok {
			return nil, true, errors.New(goSourceFinalizerRefusal)
		}
	}
	if !r.goSourceFinalizerSignature(fn, ptr) {
		return nil, true, errors.New(goSourceFinalizerRefusal)
	}
	table.mu.Lock()
	if table.armed[key] != nil {
		table.mu.Unlock()
		r.goSourceFinalizerThrow("runtime.SetFinalizer: finalizer already set")
		return nil, true, errBashPPScalarInterrupted
	}
	fin := &goSourceFinalizer{fn: fn, elem: ptr.elem}
	table.armed[key] = fin
	table.mu.Unlock()
	// The host finalizer receives the cell itself and captures only the table
	// and the record, so it never keeps the object reachable. A record whose
	// function captures the object keeps it reachable, exactly as in Go.
	runtime.SetFinalizer(ptr.target, func(cell *bashPPCell) {
		table.mu.Lock()
		defer table.mu.Unlock()
		if table.armed[key] != fin {
			return
		}
		delete(table.armed, key)
		table.ready = append(table.ready, goSourceFinalizerRun{fin: fin, target: cell})
	})
	return nil, true, nil
}

// goSourceFinalizerCallable reports a finalizer operand naming an original
// function without evaluating anything: a function or closure variable, a
// method expression on a declared type, or a method value of a local value.
func (r *Runner) goSourceFinalizerCallable(expr syntax.BashPPExpr) bool {
	for {
		paren, ok := expr.(*syntax.BashPPParenExpr)
		if !ok {
			break
		}
		expr = paren.X
	}
	if r.bashPPNativeExpr(expr) {
		return false
	}
	switch x := expr.(type) {
	case *syntax.BashPPIdent:
		return true
	case *syntax.BashPPSelectorExpr:
		if x.MethodValue {
			return !r.bashPPNativeExpr(x.X)
		}
		_, ok := r.goSourceMethodExprType(x.X)
		return ok
	}
	return false
}

func (r *Runner) goSourceFinalizerMethodExpr(expr syntax.BashPPExpr) (syntax.BashPPTypeExpr, *syntax.Lit, bool) {
	for {
		paren, ok := expr.(*syntax.BashPPParenExpr)
		if !ok {
			break
		}
		expr = paren.X
	}
	sel, ok := expr.(*syntax.BashPPSelectorExpr)
	if !ok || sel.MethodValue {
		return nil, nil, false
	}
	recv, ok := r.goSourceMethodExprType(sel.X)
	return recv, sel.Sel, ok
}

// goSourceFinalizerLit reports the function literal a finalizer argument
// spells, through parentheses.
func goSourceFinalizerLit(expr syntax.BashPPExpr) *syntax.BashPPFuncLit {
	for {
		switch x := expr.(type) {
		case *syntax.BashPPParenExpr:
			expr = x.X
		case *syntax.BashPPFuncLit:
			return x
		default:
			return nil
		}
	}
}

// goSourceFinalizerClosure builds the finalizer literal's closure over its
// free variables only. A Go closure captures just the variables it names;
// capturing the whole visible scope would keep every other local of the frame
// — the finalized object included — reachable for as long as the finalizer is
// armed, so it could never run. The closure is not entered in the closure
// registry, which retains every closure it holds for the life of the Runner.
func (r *Runner) goSourceFinalizerClosure(lit *syntax.BashPPFuncLit) *bashPPFunc {
	w := goSourceNameWalker{names: map[string]bool{}, seen: map[uintptr]bool{}}
	w.walk(reflect.ValueOf(lit))
	return &bashPPFunc{lit: lit, scope: r.bashPPScope.narrowed(w.names), typeArgs: r.bashPPTypeParamArgs}
}

// narrowed copies the scope chain keeping only the bindings named in names.
// Like snapshot it shares the cells, so a captured variable is the original.
func (s *bashPPScope) narrowed(names map[string]bool) *bashPPScope {
	if s == nil {
		return nil
	}
	out := newBashPPScope(s.parent.narrowed(names))
	for name, cell := range s.entries {
		if names[name] {
			out.put(name, cell)
		}
	}
	return out
}

// goSourceFinalizerSignature reports a finalizer with exactly one parameter
// that accepts the object's pointer: the same pointer type, or an interface.
func (r *Runner) goSourceFinalizerSignature(fn *bashPPFunc, ptr *bashPPPointer) bool {
	if fn == nil || fn.native != nil || fn.foreign != nil {
		return false
	}
	params := bashppParams(fn.params())
	if len(params)-fn.skipArgs != 1 || params[len(params)-1].variadic {
		return false
	}
	param := params[len(params)-1].typ
	if param == nil {
		return false
	}
	if _, iface := r.bashPPInterfaceType(param); iface {
		return true
	}
	want := bashPPTypeText(&syntax.BashPPPointerType{Element: ptr.elem})
	return strings.TrimPrefix(bashPPTypeText(param), "main.") == strings.TrimPrefix(want, "main.")
}

func (r *Runner) goSourceFinalizerThrow(message string) {
	r.errf("fatal error: %s\n", message)
	r.exit = exitStatus{code: bashPPPanicStatus, fatalExit: true, exiting: true}
}

// goSourceCollectFinalizers completes a host collection for runtime.GC and
// runs the finalizers it made ready. Even without armed finalizers, source GC
// must collect this heap: the dependency runtime cannot collect interpreter
// values in this process.
func (r *Runner) goSourceCollectFinalizers(ctx context.Context, call *syntax.BashPPCall) error {
	c := r.bashPPConcurrent
	if c == nil {
		runtime.GC()
		r.sweepBridgeOrigins()
		return nil
	}
	c.mu.Lock()
	table := c.finalizers
	c.mu.Unlock()
	if table == nil {
		runtime.GC()
		r.sweepBridgeOrigins()
		return nil
	}
	table.mu.Lock()
	idle := len(table.armed) == 0 && len(table.ready) == 0
	table.mu.Unlock()
	if idle {
		runtime.GC()
		r.sweepBridgeOrigins()
		return nil
	}
	goSourceHostCollect()
	r.sweepBridgeOrigins()
	table.mu.Lock()
	runs := table.ready
	table.ready = nil
	table.mu.Unlock()
	if len(runs) == 0 {
		return nil
	}
	// The finalizer goroutine shares the variables its functions name, as a
	// go statement's goroutine does; see gosource_task_capture.go.
	shared := map[*bashPPCell]bool{}
	for _, run := range runs {
		body, params := bashPPGoSourceFuncBody(run.fin.fn.lit, run.fin.fn.decl)
		set, ok := r.bashPPGoSourceCaptureSet(call, body, params, run.fin.fn.scope)
		if !ok {
			return errBashPPScalarInterrupted
		}
		for cell := range set {
			shared[cell] = true
		}
	}
	r.goSourceRunFinalizers(ctx, table, runs, shared)
	return nil
}

// goSourceHostCollect runs a full host collection and waits until the host
// finalizers it queued have run. Host finalizers run in queue batches on one
// goroutine; a sentinel queued by a later collection runs only after the
// earlier batch completed.
func goSourceHostCollect() {
	runtime.GC()
	done := make(chan struct{})
	sentinel := new([16]byte)
	runtime.SetFinalizer(sentinel, func(*[16]byte) { close(done) })
	sentinel = nil
	runtime.GC()
	select {
	case <-done:
	case <-time.After(time.Second):
	}
}

// goSourceRunFinalizers starts one goroutine that runs the ready finalizers
// in order, as Go's single finalizer goroutine does. A previous batch that is
// still running finishes first, so finalizers never overlap each other.
func (r *Runner) goSourceRunFinalizers(ctx context.Context, table *goSourceFinalizers, runs []goSourceFinalizerRun, shared map[*bashPPCell]bool) {
	c := r.bashPPConcurrency(ctx)
	table.mu.Lock()
	previous := table.running
	finished := make(chan struct{})
	table.running = finished
	table.mu.Unlock()
	state, ok := c.add()
	if !ok {
		close(finished)
		return
	}
	ordinal := state.ordinal
	child, err := r.bashPPTaskSnapshot(ordinal, shared)
	if err != nil {
		if child != nil {
			child.closeBashPPTaskResources()
		}
		close(finished)
		c.done(ordinal, &bashPPTaskFailure{ordinal: ordinal, code: 2, text: fmt.Sprintf("task snapshot: %v", err)})
		return
	}
	child.bashPPTaskState = state
	go func() {
		var failure *bashPPTaskFailure
		defer func() {
			if x := recover(); x != nil {
				failure = &bashPPTaskFailure{ordinal: ordinal, code: 2, text: fmt.Sprintf("panic: %v", x)}
			}
			child.closeBashPPTaskResources()
			close(finished)
			c.done(ordinal, failure)
		}()
		if previous != nil {
			// Waiting for the previous batch is a blocking operation: announce
			// it, so a finalizer that itself calls runtime.GC is not waited on
			// by the batch it would wait for.
			c.arm(state)
			select {
			case <-previous:
			case <-c.ctx.Done():
				return
			}
		}
		child.fillExpandConfig(c.ctx)
		for _, run := range runs {
			if c.ctx.Err() != nil {
				return
			}
			ptr := child.bashPPPointerForStorage(run.target, run.fin.elem, true)
			child.bashPPCallCells = []*bashPPCell{bashPPPointerCell(ptr)}
			child.bashPPInvoke(c.ctx, run.fin.fn, []string{""})
			if child.bashPPTaskCanceled || errors.Is(child.exit.err, context.Canceled) {
				return
			}
			if code := child.exit.code; code != 0 || child.bashPPPanicking() {
				if code == 0 {
					code = bashPPPanicStatus
				}
				text := fmt.Sprintf("exit status %d", code)
				if child.exit.fatalExit && child.exit.err != nil && !errors.Is(child.exit.err, errBashPPScalarInterrupted) {
					text = child.exit.err.Error()
				}
				failure = &bashPPTaskFailure{ordinal: ordinal, code: code, text: text}
				return
			}
		}
	}()
	// The launching goroutine continues once the finalizer goroutine's first
	// statement completed or announced that it blocks, as for a go statement.
	<-state.ready
}
