// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

const bashPPGoErrorDecoratorName = "go.error"

// Call is the decorator context: the one value every decorator receives,
// whether it is a Bash++ function whose first parameter is the predeclared
// `*Call` or a native [DecoratorFunc] from the [Decorators] registry.
//
// The shape is deliberately go-decorator's, not Python's. A decorator never
// sees or changes the target's signature; it observes the call through this
// context and decides whether the rest of the chain runs by calling [Call.Next].
// Skipping Next skips the body: the target then yields zero results and
// whatever Status the decorator left behind.
type Call struct {
	// Name is the declared name of the decorated function; a method is
	// spelled Type.Method.
	Name string
	// Site is the file:line of the call being decorated, and Caller the
	// function it was made from ("main" at the top level).
	Site   string
	Caller string
	// Args are `"$@"` strings for a shell function and typed value cells for
	// a typed function. Positions are never collapsed, including interface,
	// channel, pointer, map, and function-valued arguments.
	Args []any
	// Results are the target's result values once Next has run. A decorator
	// may rewrite them; they are type-checked back into the declared result
	// cells when the chain completes.
	Results []any
	// Status is the exit status the call will report.
	Status int
	// Agentic reports whether the DECLARATION is marked agentic. It is
	// the declaration's contract, not the caller frame's state.
	Agentic bool
	// Advised is the rule id when this decorator was applied by [Advice]
	// rather than written in source, and empty otherwise.
	Advised string

	chain *bashPPDecoratorChain
}

// Next runs the next decorator in the chain, or the body when this is the
// innermost decorator. It may be called more than once — a retry decorator
// does exactly that — and each call re-runs everything inside it.
func (c *Call) Next(ctx context.Context) {
	if c.chain != nil {
		c.chain.next(ctx)
	}
}

// Run evaluates src as shell source in the decorated call's frame — the
// callee's variable environment as the decorators see it, the runner's
// working directory, and the call's current Args as $1..$n — through the
// runner's own exec-handler chain, so whatever governs the body (an effect
// cap, a registered command, dry-run, auditing) governs src identically. src
// runs in a nested variable scope: it reads the frame's variables, and its
// own assignments do not leak back. vars are bound in that scope first.
//
// It returns src's exit status; a parse error is status 2 with the
// diagnostic on the runner's stderr. An `exit` inside src exits the shell,
// as it would anywhere. Run exists for contract-style decorators whose
// argument is a shell CHECK rather than a value (a precondition on $1, a
// postcondition on a result); it is not a way to change the target.
func (c *Call) Run(ctx context.Context, src string, vars map[string]string) int {
	if c == nil || c.chain == nil {
		return 1
	}
	r := c.chain.r
	p := syntax.NewParser()
	if r.Dialect() == syntax.LangBashPP {
		syntax.Variant(syntax.LangBashPP)(p)
	}
	file, err := p.Parse(strings.NewReader(src), "check")
	if err != nil {
		r.errf("%s: %v\n", c.Name, err)
		return 2
	}
	saved := r.bashPPDecoratorFrame()
	savedExit := r.exit
	r.bashPPRestoreDecoratorFrame(c.chain.frame)
	params := make([]string, len(c.Args))
	for i, value := range c.Args {
		params[i] = bashPPDecoratorValueText(value)
	}
	r.Params = params
	r.inFunc = true
	// A plain overlay (not a function scope): every assignment src makes
	// lands in the overlay and is discarded with it, like a subshell's.
	r.writeEnv = &overlayEnviron{parent: c.chain.frame.writeEnv}
	if r.bashPPScope != nil {
		r.bashPPScope = newBashPPScope(r.bashPPScope)
	}
	names := make([]string, 0, len(vars))
	for name := range vars {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		r.setVarString(name, vars[name])
	}
	r.exit = exitStatus{}
	r.stmts(ctx, file.Stmts)
	status := int(r.exit.code)
	exiting := r.exit.exiting
	r.bashPPRestoreDecoratorFrame(saved)
	if !exiting {
		r.exit = savedExit
	}
	return status
}

// DecoratorArg is one evaluated argument of a decorator line, positional
// when Name is empty.
type DecoratorArg struct {
	Name  string
	Value string
}

// DecoratorFunc is a native decorator. It is resolved by name at call time,
// after any Bash++ function of the same name, so a script may shadow it.
// A non-nil error aborts the call with status 1.
type DecoratorFunc func(ctx context.Context, c *Call, args []DecoratorArg) error

// Decorators registers native decorators by name, mirroring [CommandResolver]:
// the engine exposes only the slot, and every implementation lives with the
// embedder.
func Decorators(m map[string]DecoratorFunc) RunnerOption {
	return func(r *Runner) error {
		r.bashPPNativeDecorators = m
		return nil
	}
}

// DecoratorSpec is one decorator line applied by policy advice: the rule id
// it came from, the decorator to resolve, and its already-evaluated args.
type DecoratorSpec struct {
	ID   string
	Name string
	Args []DecoratorArg
}

// AdviceFunc is consulted each time a function is registered — by
// declaration, `eval`, `source` or redefinition — with its name, source file
// and agentic mark, and answers the decorators policy adds to it.
type AdviceFunc func(name, file string, agentic bool) []DecoratorSpec

// Advice installs registration-time policy advice. Advised decorators are
// add-only and outermost: they wrap every source decorator and never
// interpose between an author's decorator and the body. Application is
// idempotent by rule id, so re-registering never stacks duplicates, and
// `declare -f` keeps printing source decorators only.
func Advice(fn AdviceFunc) RunnerOption {
	return func(r *Runner) error {
		r.bashPPAdvice = fn
		return nil
	}
}

// bashPPDecorated is the side-table entry for a decorated SHELL function. A
// typed function carries its decorators on its declaration; a shell function
// stores only its body in [Runner.Funcs], which embedders may replace, so the
// entry keeps the body identity exactly as the agentic table does.
type bashPPDecorated struct {
	body       *syntax.Stmt
	decorators []*syntax.BashPPDecorator
	advised    []DecoratorSpec
}

// bashPPDecoratorRung is one resolved-at-call-time link of a chain.
type bashPPDecoratorRung struct {
	name     string
	args     []*syntax.Word
	argNames []*syntax.Lit
	// literal holds pre-evaluated advised arguments; args is nil then.
	literal []DecoratorArg
	advised string
}

// bashPPDecoratorFrame is the target frame's execution context, captured when
// the chain starts and put back for the body: a decorator runs in its own
// ordinary frame, so by the time its Next reaches the body the runner is
// positioned in the decorator's scope, not the target's.
type bashPPDecoratorFrame struct {
	params   []string
	inFunc   bool
	writeEnv expand.WriteEnviron
	scope    *bashPPScope
	agentic  bool
	ret      bashPPReturnState
	typeArgs map[string]syntax.BashPPTypeExpr
	filename string
}

func (r *Runner) bashPPDecoratorFrame() bashPPDecoratorFrame {
	return bashPPDecoratorFrame{
		params: r.Params, inFunc: r.inFunc, writeEnv: r.writeEnv, scope: r.bashPPScope,
		agentic: r.bashPPAgentic, ret: r.bashPPReturn, typeArgs: r.bashPPTypeParamArgs,
		filename: r.filename,
	}
}

func (r *Runner) bashPPRestoreDecoratorFrame(f bashPPDecoratorFrame) {
	r.Params, r.inFunc, r.writeEnv, r.bashPPScope = f.params, f.inFunc, f.writeEnv, f.scope
	r.bashPPAgentic, r.bashPPReturn, r.bashPPTypeParamArgs = f.agentic, f.ret, f.typeArgs
	r.filename = f.filename
}

// bashPPDecoratorChain is one decorated invocation in flight.
type bashPPDecoratorChain struct {
	r     *Runner
	call  *Call
	rungs []bashPPDecoratorRung
	// depth is the rung Next will run; len(rungs) means the body.
	depth int
	// cell is the script-visible Call struct, addressed by ptr; it exists
	// only once a Bash++ decorator needs it.
	cell *bashPPCell
	ptr  *bashPPPointer
	// body runs the target body in the target frame and reports its status
	// and settled results.
	body func(ctx context.Context) (int, []string)
	// frame is the target's context, restored around the body.
	frame bashPPDecoratorFrame
	// resultNames lets the body boundary re-read named results after defers.
	resultNames []string
	// ctxs is the context each open Next was entered with, innermost last,
	// so a rung that cancelled its own child context can be told apart from
	// a cancellation of the chain's caller.
	ctxs []context.Context
	// declScope is the TARGET's captured declaration scope for a typed
	// target, and nil for a shell one: a typed rung's arguments evaluate
	// there on every invocation, so neither a decorator's locals nor the
	// target's bound parameters are visible to them, while a shell target
	// keeps the dynamic scoping shell functions already have.
	declScope *bashPPScope
	// stack is the decorator names executing when the chain began; cycles
	// are detected against it, and the body runs with it restored so a
	// decorated function called from a decorated body is not a cycle.
	stack []string
	// targetDepth is the target frame's index on the call stack. The body
	// runs with that frame rotated to the top, so FUNCNAME[0] is still the
	// decorated function and FUNCNAME[1] the innermost decorator.
	targetDepth int
	bodyRan     bool
	failed      bool
}

const bashPPDecoratorCallType = "Call"

// bashPPPredeclaredCallSource is the predeclared Call as the script sees it.
// Args and Results hold one entry per position, preserved by index: a plain
// value is its shell string, while a structured, pointer, channel, interface
// or function-valued position keeps its typed cell.
const bashPPPredeclaredCallSource = `type Call struct {
	Name string
	Site string
	Caller string
	Args []any
	Results []any
	Status int
	Agentic bool
	Advised string
}
func (c *Call) Next() { }
`

// bashPPEnsureCallType installs the predeclared Call type and its Next method
// on first use, unless the script declared its own Call, which shadows the
// predeclared one exactly as a declaration shadows `any`.
func (r *Runner) bashPPEnsureCallType(ctx context.Context) bool {
	if _, exists := r.bashPPTypes[bashPPDecoratorCallType]; exists {
		return r.bashPPPredeclaredCall
	}
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(bashPPPredeclaredCallSource), "")
	if err != nil {
		r.errf("BASHPP-EDECO-CALL: predeclared Call is unavailable: %v\n", err)
		r.exit.code = 1
		return false
	}
	saved := r.exit
	for _, stmt := range f.Stmts {
		switch cm := stmt.Cmd.(type) {
		case *syntax.BashPPDecl:
			r.bashPPDeclare(ctx, cm)
		case *syntax.BashPPFuncDecl:
			r.bashPPFuncDecl(cm)
		}
	}
	if r.exit.code != 0 {
		return false
	}
	r.exit = saved
	if next := r.bashPPMethods[bashPPDecoratorCallType]["Next"]; next != nil {
		next.decoratorNext = true
	}
	r.bashPPPredeclaredCall = true
	return true
}

// bashPPShadowPredeclaredCall drops the predeclared Call so a script's own
// `type Call` can take the name. Decorators that named the predeclared type
// stop being decorators, which is the EDECO-SIG diagnostic at their next use.
func (r *Runner) bashPPShadowPredeclaredCall(name string) {
	if name != bashPPDecoratorCallType || !r.bashPPPredeclaredCall {
		return
	}
	r.bashPPUnshareTypes()
	r.bashPPUnshareMethods()
	delete(r.bashPPTypes, name)
	delete(r.bashPPMethods, name)
	r.bashPPPredeclaredCall = false
}

// bashPPDecoratorSignature reports whether fn is a decorator: its first
// parameter is the predeclared *Call.
func (r *Runner) bashPPDecoratorSignature(fn *bashPPFunc) bool {
	if fn == nil || fn.decl == nil || fn.decl.Receiver != nil {
		return false
	}
	params := bashppParams(fn.params())
	if len(params) == 0 || params[0].variadic {
		return false
	}
	ptr, ok := params[0].typ.(*syntax.BashPPPointerType)
	if !ok {
		return false
	}
	named, ok := ptr.Element.(*syntax.BashPPNamedType)
	if !ok || named.Name == nil || named.Name.Value != bashPPDecoratorCallType || len(named.TypeArgs) > 0 {
		return false
	}
	// A script-declared Call is not the predeclared one.
	if _, exists := r.bashPPTypes[bashPPDecoratorCallType]; exists && !r.bashPPPredeclaredCall {
		return false
	}
	return true
}

// bashPPRegisterDecorators validates a declaration's decorator stack and
// records any advice for name. It reports false when the declaration must be
// refused.
func (r *Runner) bashPPRegisterDecorators(name string, pos syntax.Pos, decorators []*syntax.BashPPDecorator, agentic bool) ([]DecoratorSpec, bool) {
	goError := false
	for _, d := range decorators {
		if d.Name.Value == bashPPGoErrorDecoratorName {
			if goError || len(d.Args) != 0 || len(d.ArgNames) != 0 {
				r.errf("%sBASHPP-EDECO-GOERROR: @go.error requires no arguments and may appear once\n", r.bashErrPrefix(d.Pos()))
				r.exit.code = 2
				return nil, false
			}
			goError = true
			continue
		}
		if strings.Contains(d.Name.Value, ".") {
			r.errf("%sBASHPP-EDECO-RESERVED: @%s: namespaced decorators are reserved\n", r.bashErrPrefix(d.Pos()), d.Name.Value)
			r.exit.code = 2
			return nil, false
		}
		if d.Name.Value == name {
			r.errf("%sBASHPP-EDECO-SELF: %s cannot decorate itself\n", r.bashErrPrefix(d.Pos()), name)
			r.exit.code = 2
			return nil, false
		}
	}
	if r.bashPPAdvice == nil {
		return nil, true
	}
	var advised []DecoratorSpec
	seen := make(map[string]bool)
	for _, spec := range r.bashPPAdvice(name, r.filename, agentic) {
		if spec.Name == "" || spec.ID == "" || seen[spec.ID] {
			continue
		}
		seen[spec.ID] = true
		advised = append(advised, spec)
	}
	return advised, true
}

// bashPPDecoratedShellFunc returns the side-table entry for name when its
// body is still the registered one.
func (r *Runner) bashPPDecoratedShellFunc(name string) *bashPPDecorated {
	entry := r.bashPPDecoratedFuncs[name]
	if entry == nil || entry.body != r.Funcs[name] {
		return nil
	}
	return entry
}

func bashPPDecoratorRungs(decorators []*syntax.BashPPDecorator, advised []DecoratorSpec) []bashPPDecoratorRung {
	rungs := make([]bashPPDecoratorRung, 0, len(decorators)+len(advised))
	for _, spec := range advised {
		rungs = append(rungs, bashPPDecoratorRung{name: spec.Name, literal: spec.Args, advised: spec.ID})
	}
	for _, d := range decorators {
		if d.Name.Value == bashPPGoErrorDecoratorName {
			continue
		}
		rungs = append(rungs, bashPPDecoratorRung{name: d.Name.Value, args: d.Args, argNames: d.ArgNames})
	}
	return rungs
}

func bashPPGoErrorDecorated(d *syntax.BashPPFuncDecl) bool {
	for _, dec := range d.Decorators {
		if dec.Name.Value == bashPPGoErrorDecoratorName {
			return true
		}
	}
	return false
}

func bashPPGoErrorResultField() *syntax.BashPPField {
	return &syntax.BashPPField{FieldType: &syntax.Lit{Value: "error"}}
}

func bashPPTrailingErrorResult(results []*syntax.BashPPField) bool {
	if len(results) == 0 {
		return false
	}
	last := results[len(results)-1]
	if last.FieldType != nil && last.FieldType.Value == "error" {
		return true
	}
	named, ok := last.FieldTypeExpr.(*syntax.BashPPNamedType)
	return ok && named.Name != nil && named.Name.Value == "error" && len(named.TypeArgs) == 0
}

func bashPPGoErrorCell(name string, status int) (*bashPPCell, string) {
	// Normalize to the 8-bit range `$?` reports (r.exit.code truncates the same
	// value to uint8), so the minted error's presence and message agree with
	// `$?` and with the lowered shellrt.GoError for any decorator-set status.
	status = int(uint8(status))
	errorType := &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "error"}}
	if status == 0 {
		cell := &bashPPCell{vr: expand.Variable{Set: true, Kind: expand.String}, declType: errorType}
		cell.interfaceValue = &bashPPInterfaceValue{nilIface: true}
		return cell, ""
	}
	text := fmt.Sprintf("%s: exit status %d", name, status)
	payload := &bashPPCell{vr: expand.Variable{Set: true, Kind: expand.String, Str: text}, declType: &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "string"}}}
	cell := &bashPPCell{vr: expand.Variable{Set: true, Kind: expand.String, Str: text}, declType: errorType}
	cell.interfaceValue = &bashPPInterfaceValue{cell: payload, dynamic: payload.declType}
	return cell, text
}

// bashPPNewDecoratorChain prepares the chain for one invocation. It is called
// from inside the target frame — after the agentic, argument and FUNCNEST
// gates and inside the trap bracket — so the frame it captures is the one the
// body must run in.
func (r *Runner) bashPPNewDecoratorChain(name string, rungs []bashPPDecoratorRung, args []string, cells []*bashPPCell, agentic bool) *bashPPDecoratorChain {
	values := make([]any, len(args))
	for i, arg := range args {
		values[i] = arg
		if i < len(cells) && cells[i] != nil {
			values[i] = bashPPDecoratorCellValue(cells[i])
		}
	}
	call := &Call{Name: name, Args: values, Agentic: agentic}
	pos := r.curStmtPos
	if pos.IsValid() {
		call.Site = fmt.Sprintf("%s:%d", r.filename, pos.Line())
	}
	call.Caller = "main"
	// The target's own frame is already on the stack; its caller is the one
	// below it.
	if n := len(r.callStack); n >= 2 {
		call.Caller = r.callStack[n-2].funcName
	}
	chain := &bashPPDecoratorChain{r: r, call: call, rungs: rungs, frame: r.bashPPDecoratorFrame(),
		stack: append([]string(nil), r.bashPPDecoratorStack...), targetDepth: len(r.callStack) - 1}
	call.chain = chain
	return chain
}

// run executes the whole chain, outermost decorator first, and reports
// whether it completed. On completion call.Status and call.Results are the
// outcome; the caller installs them.
func (c *bashPPDecoratorChain) run(ctx context.Context) bool {
	c.next(ctx)
	return !c.failed
}

// next is Call.Next: it runs the rung at the current depth, or the body past
// the last one, then restores the depth so the same decorator may call it
// again.
func (c *bashPPDecoratorChain) next(ctx context.Context) {
	r := c.r
	if c.failed || r.exit.exiting || r.bashPPPanicking() {
		return
	}
	depth := c.depth
	c.depth++
	advised := c.call.Advised
	c.ctxs = append(c.ctxs, ctx)
	defer func() {
		c.depth = depth
		// A rung that ran Next under its own derived context (a timeout
		// decorator) owns that context's cancellation: when the runner went
		// fatal on exactly that error and the rung's own context is still
		// live, the fatal unwinds no further than the rung, which seals the
		// call with the status it chooses. A cancellation from outside the
		// chain keeps unwinding.
		c.ctxs = c.ctxs[:len(c.ctxs)-1]
		if r.exit.fatalExit && ctx.Err() != nil && errors.Is(r.exit.err, ctx.Err()) {
			outer := context.Background()
			if n := len(c.ctxs); n > 0 {
				outer = c.ctxs[n-1]
			}
			if outer.Err() == nil {
				r.exit = exitStatus{code: r.exit.code}
			}
		}
		// The rung this Next entered set Advised for its own run; the rung
		// that resumes after Next is the one this restores, on the context
		// and in the script-visible struct alike.
		c.call.Advised = advised
		c.sync()
	}()
	if depth >= len(c.rungs) {
		c.runBody(ctx)
		return
	}
	rung := c.rungs[depth]
	c.call.Advised = rung.advised
	// Policy advice is trusted native code; source declarations cannot
	// replace it, even when an explicit decorator uses the same name.
	if rung.advised != "" {
		if native := r.bashPPNativeDecorators[rung.name]; native != nil {
			c.runNative(ctx, rung, native)
		} else {
			c.fail("BASHPP-EDECO-UNDEF: native policy decorator %s is not defined\n", rung.name)
		}
		return
	}
	// A cycle is a decorator's own chain reaching a decorator that is
	// already running: the target is on the decorator stack and so is the
	// rung. The same decorator stacked twice on one declaration is not a
	// cycle, nor is a decorated function called from a decorated body.
	if bashPPDecoratorActive(r.bashPPDecoratorStack, c.call.Name) && bashPPDecoratorActive(r.bashPPDecoratorStack, rung.name) {
		c.fail("BASHPP-EDECO-CYCLE: decorator %s is already decorating an active call\n", rung.name)
		return
	}
	if fn := r.bashPPFuncs[rung.name]; fn != nil {
		if !r.bashPPDecoratorSignature(fn) {
			c.fail("BASHPP-EDECO-SIG: %s is not a decorator: its first parameter must be *Call\n", rung.name)
			return
		}
		c.runScripted(ctx, rung, fn)
		return
	}
	if native := r.bashPPNativeDecorators[rung.name]; native != nil {
		c.runNative(ctx, rung, native)
		return
	}
	if r.Funcs[rung.name] != nil {
		c.fail("BASHPP-EDECO-SIG: %s is not a decorator: a shell function has no *Call parameter\n", rung.name)
		return
	}
	if rung.name == bashPPTimedDecoratorName {
		c.runNative(ctx, rung, bashPPTimedDecorator(r))
		return
	}
	c.fail("BASHPP-EDECO-UNDEF: decorator %s is not defined\n", rung.name)
}

func bashPPDecoratorActive(stack []string, name string) bool {
	for _, active := range stack {
		if active == name {
			return true
		}
	}
	return false
}

// fail records a decorator diagnostic with status 1 and marks the chain
// failed. The failure sequence lets an enclosing chain — one whose decorator
// is itself decorated — notice that a nested chain failed inside an
// otherwise ordinary frame.
func (c *bashPPDecoratorChain) fail(format string, args ...any) {
	r := c.r
	r.errf(format, args...)
	r.exit.code = 1
	r.bashPPDecoratorFailSeq++
	c.failed = true
}

// runBody runs the target in its own frame and records the outcome on the
// context. Each body invocation is an ordinary call boundary: its deferred
// calls run before Next returns to the innermost decorator, including while a
// panic is unwinding. This is also what makes repeated Next calls independent
// invocations rather than one frame with an accumulated defer stack.
func (c *bashPPDecoratorChain) runBody(ctx context.Context) {
	r := c.r
	decorator := r.bashPPDecoratorFrame()
	stack := r.bashPPDecoratorStack
	r.bashPPDecoratorStack = c.stack
	r.bashPPRestoreDecoratorFrame(c.frame)
	top := len(r.callStack) - 1
	rotated := c.targetDepth >= 0 && c.targetDepth < top
	if rotated {
		target := r.callStack[c.targetDepth]
		copy(r.callStack[c.targetDepth:], r.callStack[c.targetDepth+1:top+1])
		r.callStack[top] = target
	}
	mark := len(r.bashPPDeferStack)
	status, results := c.body(ctx)
	if !r.exit.exiting {
		r.bashPPRunDefers(ctx, mark)
	} else if !r.bashPPTestingCancelUnwind(ctx, mark) {
		r.bashPPDeferStack = r.bashPPDeferStack[:mark]
	}
	if !c.failed && !r.exit.exiting && !r.bashPPPanicking() {
		results = r.bashPPFinalResults(results, c.resultNames)
		c.call.Status = status
		c.call.Results = make([]any, len(results))
		for i, result := range results {
			c.call.Results[i] = result
			var source *bashPPCell
			if i < len(c.resultNames) && c.resultNames[i] != "" {
				source = r.bashPPScope.lookup(c.resultNames[i])
			} else if i < len(r.bashPPReturn.cells) {
				source = r.bashPPReturn.cells[i]
			}
			if source != nil {
				c.call.Results[i] = bashPPDecoratorCellValue(source)
			}
		}
		r.exit = exitStatus{}
		c.sync()
	}
	if rotated && len(r.callStack) == top+1 {
		target := r.callStack[top]
		copy(r.callStack[c.targetDepth+1:top+1], r.callStack[c.targetDepth:top])
		r.callStack[c.targetDepth] = target
	}
	r.bashPPRestoreDecoratorFrame(decorator)
	r.bashPPDecoratorStack = stack
	c.bodyRan = true
	if c.failed || r.exit.exiting || r.bashPPPanicking() {
		return
	}
}

// decoratorArgScope positions the runner's typed scope for evaluating a
// rung's argument words: a typed target's captured declaration scope, or the
// current (dynamic) scope for a shell target. It returns the scope to restore.
func (c *bashPPDecoratorChain) decoratorArgScope() *bashPPScope {
	saved := c.r.bashPPScope
	base := saved
	if c.declScope != nil {
		base = c.declScope
	}
	c.r.bashPPScope = newBashPPScope(base)
	return saved
}

// runNative invokes a registry decorator with the rung's evaluated args.
func (c *bashPPDecoratorChain) runNative(ctx context.Context, rung bashPPDecoratorRung, fn DecoratorFunc) {
	r := c.r
	args := rung.literal
	if rung.args != nil {
		args = make([]DecoratorArg, 0, len(rung.args))
		positional := len(rung.args) - len(rung.argNames)
		saved := c.decoratorArgScope()
		for i, w := range rung.args {
			arg := DecoratorArg{Value: r.bashPPExprValue(w)}
			if i >= positional {
				arg.Name = rung.argNames[i-positional].Value
			}
			args = append(args, arg)
		}
		r.bashPPScope = saved
	}
	c.load()
	r.bashPPDecoratorStack = append(r.bashPPDecoratorStack, rung.name)
	err := fn(ctx, c.call, args)
	r.bashPPDecoratorStack = r.bashPPDecoratorStack[:len(r.bashPPDecoratorStack)-1]
	if err != nil {
		c.fail("BASHPP-EDECO-NATIVE: @%s: %v\n", rung.name, err)
		return
	}
	c.sync()
}

// bashPPDecoratorContextName is the hidden binding the synthesized call
// passes as the decorator's *Call argument. It lives in a scope pushed for
// the duration of that one call, so the script never sees it.
const bashPPDecoratorContextName = "__bashpp_call"

// runScripted invokes a Bash++ decorator as an ordinary frame through
// bashPPInvoke, so defer, panic unwinding, recover and FUNCNEST all apply,
// and the frame is visible in FUNCNAME. The decorator's own arguments are
// evaluated here, per invocation, by the same binding contract as any call.
func (c *bashPPDecoratorChain) runScripted(ctx context.Context, rung bashPPDecoratorRung, fn *bashPPFunc) {
	r := c.r
	if !c.materialize(ctx) {
		c.failed = true
		return
	}
	c.store()
	callWords := []*syntax.Word{{Parts: []syntax.WordPart{&syntax.Lit{Value: bashPPDecoratorContextName}}}}
	var names []*syntax.Lit
	if rung.args != nil {
		callWords = append(callWords, rung.args...)
		names = rung.argNames
	} else {
		for _, arg := range rung.literal {
			callWords = append(callWords, &syntax.Word{Parts: []syntax.WordPart{&syntax.SglQuoted{Value: arg.Value}}})
			if arg.Name != "" {
				names = append(names, &syntax.Lit{Value: arg.Name})
			}
		}
	}
	call := &syntax.BashPPCall{Fun: []*syntax.Lit{{Value: rung.name}}, Args: callWords, ArgNames: names}

	// The rung's argument words evaluate for THIS invocation, in the typed
	// target's captured declaration scope (dynamic for a shell target); the
	// pushed child also carries the hidden *Call binding.
	scope := c.decoratorArgScope()
	ctxCell := &bashPPCell{declType: &syntax.BashPPPointerType{Element: &syntax.BashPPNamedType{Name: &syntax.Lit{Value: bashPPDecoratorCallType}}}, typeName: bashPPDecoratorCallType}
	bashPPStoreCellValue(ctxCell, c.ptr, nil)
	r.bashPPScope.put(bashPPDecoratorContextName, ctxCell)
	r.bashPPDecoratorStack = append(r.bashPPDecoratorStack, rung.name)
	failSeq := r.bashPPDecoratorFailSeq
	args, ok := r.bashPPCallValues(call, fn)
	if ok {
		r.bashPPInvoke(ctx, fn, args)
	}
	r.bashPPDecoratorStack = r.bashPPDecoratorStack[:len(r.bashPPDecoratorStack)-1]
	r.bashPPScope = scope
	// A binding diagnostic, a dialect diagnostic (status 2), a nested
	// decorator failure, an exit or a panic all abort the call; the
	// decorator's ordinary last status does not, because the outcome is what
	// it left on the context.
	if !ok || r.exit.code == 2 || r.exit.exiting || r.exit.fatalExit || r.bashPPPanicking() || failSeq != r.bashPPDecoratorFailSeq {
		c.failed = true
		return
	}
	r.exit = exitStatus{}
	c.load()
}

// materialize creates the script-visible Call on first need.
func (c *bashPPDecoratorChain) materialize(ctx context.Context) bool {
	r := c.r
	if c.cell != nil {
		return true
	}
	if !r.bashPPEnsureCallType(ctx) {
		return false
	}
	lit := &syntax.BashPPCompositeLit{LitType: &syntax.BashPPNamedType{Name: &syntax.Lit{Value: bashPPDecoratorCallType}}}
	ptr, err := r.bashPPCompositeAddress(lit)
	if err != nil {
		r.errf("BASHPP-EDECO-CALL: %v\n", err)
		r.exit.code = 1
		return false
	}
	c.ptr, c.cell = ptr, ptr.target
	if r.bashPPDecoratorChains == nil {
		r.bashPPDecoratorChains = make(map[*bashPPCell]*bashPPDecoratorChain)
	}
	r.bashPPDecoratorChains[c.cell] = c
	return true
}

func (c *bashPPDecoratorChain) release() {
	if c.cell != nil {
		delete(c.r.bashPPDecoratorChains, c.cell)
	}
}

// store writes the Go-side context into the script-visible struct.
func (c *bashPPDecoratorChain) store() {
	if c.cell == nil {
		return
	}
	obj, _ := c.cell.vr.Obj.(map[string]any)
	meta := bashPPCellMeta(c.cell)
	if obj == nil || meta == nil {
		return
	}
	set := func(field string, value any, child *bashPPCollectionMeta) {
		bashPPStorageSetField(obj, meta.mapping, field, value, child)
	}
	values := func(field string, values []any) {
		typ := &syntax.BashPPCollectionType{Kind: "slice", Element: &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "any"}}}
		child := &bashPPCollectionMeta{kind: "slice", typ: typ, sequence: make([]*bashPPCollectionMeta, len(values))}
		seq := make([]any, len(values))
		for i, v := range values {
			if cell, ok := v.(*bashPPCell); ok {
				seq[i] = cell.vrValue()
				child.sequence[i] = &bashPPCollectionMeta{kind: "interface", typ: typ.Element, interfaceValue: &bashPPInterfaceValue{dynamic: cell.declType, cell: cell}}
			} else {
				seq[i] = v
			}
		}
		set(field, seq, child)
	}
	set("Name", c.call.Name, nil)
	set("Site", c.call.Site, nil)
	set("Caller", c.call.Caller, nil)
	set("Status", c.call.Status, nil)
	set("Agentic", c.call.Agentic, nil)
	set("Advised", c.call.Advised, nil)
	values("Args", c.call.Args)
	values("Results", c.call.Results)
}

// load reads the fields a decorator may have rewritten back into the
// Go-side context.
func (c *bashPPDecoratorChain) load() {
	if c.cell == nil {
		return
	}
	obj, _ := c.cell.vr.Obj.(map[string]any)
	if obj == nil {
		return
	}
	if v, ok := bashPPStorageGet(obj, "Status"); ok {
		c.call.Status = bashPPDecoratorInt(v)
	}
	if v, ok := bashPPStorageGet(obj, "Results"); ok {
		c.call.Results = c.loadValues("Results", v)
	}
	if v, ok := bashPPStorageGet(obj, "Args"); ok {
		c.call.Args = c.loadValues("Args", v)
	}
}

func (c *bashPPDecoratorChain) loadValues(field string, value any) []any {
	seq, _ := value.([]any)
	out := append([]any(nil), seq...)
	meta := bashPPCellMeta(c.cell)
	if meta == nil || meta.mapping[field] == nil {
		return out
	}
	for i, elem := range meta.mapping[field].sequence {
		if elem != nil && elem.interfaceValue != nil && elem.interfaceValue.cell != nil {
			out[i] = elem.interfaceValue.cell
		}
	}
	return out
}

// sync pushes the Go-side context into the struct when one exists.
func (c *bashPPDecoratorChain) sync() { c.store() }

func bashPPDecoratorInt(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(x))
		return n
	}
	n, _ := strconv.Atoi(fmt.Sprint(v))
	return n
}

func bashPPDecoratorValueCell(value any) (*bashPPCell, string) {
	if cell, ok := value.(*bashPPCell); ok && cell != nil {
		return bashPPCopyAssignmentCell(cell), fmt.Sprint(cell.vrValue())
	}
	text := bashPPDecoratorValueText(value)
	return &bashPPCell{vr: expand.Variable{Set: true, Kind: expand.String, Str: text}}, text
}

// bashPPDecoratorValueText renders one Call.Args/Results entry as the shell
// string a positional parameter or plain binding receives.
func bashPPDecoratorValueText(value any) string {
	if cell, ok := value.(*bashPPCell); ok && cell != nil {
		return fmt.Sprint(cell.vrValue())
	}
	return fmt.Sprint(value)
}

func bashPPDecoratorCellValue(cell *bashPPCell) any {
	if cell == nil {
		return nil
	}
	_, function := cell.declType.(*syntax.BashPPFuncType)
	if bashPPStructuredCell(cell) || cell.channel != nil || function {
		return bashPPCopyAssignmentCell(cell)
	}
	return cell.vrValue()
}

// bashPPDecoratorNext answers the predeclared Call.Next method: it finds the
// chain the receiver belongs to and advances it.
func (r *Runner) bashPPDecoratorNext(ctx context.Context, fn *bashPPFunc) []string {
	r.bashPPResultCells = nil
	r.bashPPCallCells, r.bashPPCallChannels, r.bashPPCallInterfaces, r.bashPPCallSpread = nil, nil, nil, false
	var chain *bashPPDecoratorChain
	if fn.receiver != nil && fn.receiver.pointerValue != nil {
		chain = r.bashPPDecoratorChains[fn.receiver.pointerValue.target]
	}
	if chain == nil {
		r.errf("BASHPP-EDECO-CALL: Next called outside a decorator chain\n")
		r.exit.code = 1
		return nil
	}
	chain.load()
	chain.next(ctx)
	if !chain.failed && !r.exit.exiting && !r.bashPPPanicking() {
		r.exit = exitStatus{}
	}
	return nil
}

// bashPPInvokeDecorated runs a typed function's body through its decorator
// chain. It returns the settled results, or false when the chain failed;
// the caller's own settle path is bypassed because the context is now the
// source of truth for status and results.
func (r *Runner) bashPPInvokeDecorated(ctx context.Context, fn *bashPPFunc, args []string, callCells []*bashPPCell, callChannels []*bashPPChannel, resultNames []string, rungs []bashPPDecoratorRung) ([]string, bool) {
	name := fn.name()
	if recv := fn.decl.Receiver; recv != nil && recv.RecvType != nil && fn.decl.Name != nil {
		name = recv.RecvType.Value + "." + fn.decl.Name.Value
	}
	chain := r.bashPPNewDecoratorChain(name, rungs, args, callCells, fn.decl.Agentic != nil)
	chain.declScope = fn.scope
	chain.resultNames = resultNames
	// An argument bound to an interface parameter keeps its typed cell in
	// Args, whatever its shape: the cell's declared type IS the dynamic type
	// a no-op chain must hand back to the body, and a plain scalar rendering
	// would erase it.
	targetParams := bashppParams(fn.params())
	for i := range chain.call.Args {
		if i >= len(callCells) || callCells[i] == nil || len(targetParams) == 0 {
			continue
		}
		param := targetParams[min(i, len(targetParams)-1)]
		if _, ok := chain.call.Args[i].(*bashPPCell); ok || param.typ == nil {
			continue
		}
		if _, iface := r.bashPPInterfaceType(param.typ); iface {
			chain.call.Args[i] = bashPPCopyAssignmentCell(callCells[i])
		}
	}
	// A channel argument's identity travels beside the cells, gated by task
	// group ownership at the call; carry it on the Args cell so a no-op chain
	// hands the body the very channel the caller passed.
	for i, channel := range callChannels {
		if channel == nil || i >= len(chain.call.Args) {
			continue
		}
		cell, ok := chain.call.Args[i].(*bashPPCell)
		if !ok {
			if i < len(callCells) && callCells[i] != nil {
				cell = bashPPCopyAssignmentCell(callCells[i])
			} else {
				cell = &bashPPCell{vr: expand.Variable{Set: true, Kind: expand.String, Str: args[i]}}
			}
			chain.call.Args[i] = cell
		}
		cell.channel, cell.channelOwner = channel, r.bashPPConcurrent
	}
	defer chain.release()
	chain.body = func(ctx context.Context) (int, []string) {
		// Every Next binds the body's parameters afresh from the context, so
		// a decorator's Args rewrite feeds the body and a repeated Next never
		// sees what the previous run left in the bindings. The rebinding
		// revalidates what the original call already proved: the arity and
		// the per-parameter types, which an Args mutation may have broken.
		r.bashPPResultCells = nil
		params := bashppParams(fn.params())
		fixed := len(params)
		variadic := fixed > 0 && params[fixed-1].variadic
		if variadic {
			fixed--
		}
		callArgs := chain.call.Args
		switch {
		case variadic && len(callArgs) < fixed:
			chain.fail("BASHPP-EDECO-ARG: %s: decorator supplied %d argument(s); expected at least %d\n", fn.name(), len(callArgs), fixed)
			return 1, nil
		case !variadic && len(callArgs) != fixed:
			chain.fail("BASHPP-EDECO-ARG: %s: decorator supplied %d argument(s); expected %d\n", fn.name(), len(callArgs), fixed)
			return 1, nil
		}
		args = make([]string, len(callArgs))
		cells := make([]*bashPPCell, len(callArgs))
		for i, value := range callArgs {
			cell, text := bashPPDecoratorValueCell(value)
			args[i], cells[i] = text, cell
			param := params[min(i, len(params)-1)]
			if _, typed := value.(*bashPPCell); typed {
				continue
			}
			if _, funcTyped := param.typ.(*syntax.BashPPFuncType); funcTyped {
				continue
			}
			if param.declared != "" && !r.bashPPValueFits(param.declared, text) {
				where := param.name
				if where == "" {
					where = strconv.Itoa(i + 1)
				}
				chain.fail("BASHPP-EDECO-ARG: %s: cannot use %q as %s value for parameter %s\n", fn.name(), text, param.declared, where)
				return 1, nil
			}
		}
		for i, param := range params {
			if param.variadic {
				if r.bashPPGoSource && param.name != "" {
					if !r.goSourceBindVariadic(param, args[i:], cells[min(i, len(cells)):], false) {
						chain.failed = true
						return 1, nil
					}
					break
				}
				if param.name != "" {
					rest := append([]string(nil), args[i:]...)
					cell := &bashPPCell{vr: expand.Variable{Set: true, Kind: expand.Indexed, List: rest}}
					cell.valueMeta = &bashPPCollectionMeta{
						kind:     "slice",
						typ:      &syntax.BashPPCollectionType{Kind: "slice", Element: param.typ},
						sequence: make([]*bashPPCollectionMeta, len(rest)),
					}
					r.bashPPScope.put(param.name, cell)
				}
				break
			}
			if param.name == "" {
				continue
			}
			cell := cells[i]
			if expected := param.typ; expected != nil {
				// The expected-type conversion may build a fresh cell; the
				// channel identity rides the argument, so it is put back after,
				// as the undecorated binding rebinds it from the call's own
				// channel provenance.
				channel, channelOwner := cell.channel, cell.channelOwner
				bound, err := r.goSourceExpectedCell(cell, expected)
				if err != nil {
					chain.fail("BASHPP-EDECO-ARG: %s: %v\n", fn.name(), err)
					return 1, nil
				}
				if bound == cell {
					bound = bashPPCopyAssignmentCell(bound)
				}
				bound.constant = false
				bound.vr.ReadOnly = false
				bound.vr.Exported = false
				if err := r.bashPPBindInterfaceParam(bound, expected); err != nil {
					chain.fail("BASHPP-EDECO-ARG: %s: %v\n", fn.name(), err)
					return 1, nil
				}
				bound.declType = expected
				if channel != nil {
					bound.channel, bound.channelOwner = channel, channelOwner
				}
				cell = bound
			}
			r.bashPPScope.put(param.name, cell)
			if base := strings.TrimPrefix(param.declared, "*"); base != "" {
				if _, ok := r.bashPPTypes[base]; ok {
					cell.typeName = base
					cell.pointer = r.bashPPDeclaredPointer(param.declared) || cell.pointer && r.goSourceUnsafePointerType(param.typ)
					cell.nilPointer = cell.pointer && args[i] == ""
					if r.bashPPGoSource && cell.pointer {
						cell.nilPointer = cell.pointerValue == nil
					}
				}
			}
		}
		r.Params = append([]string(nil), args...)
		if body := fn.body(); body != nil {
			r.stmts(ctx, body.Stmts)
		}
		if r.exit.exiting || r.bashPPPanicking() {
			return int(r.exit.code), nil
		}
		results := r.bashPPSettleResults(fn, resultNames)
		return int(r.exit.code), results
	}
	ok := chain.run(ctx)
	if !ok || r.exit.exiting || r.bashPPPanicking() {
		return nil, false
	}
	count := bashppResultCount(fn.bodyResults())
	resultValues := chain.call.Results
	r.exit = exitStatus{code: uint8(chain.call.Status)}
	if count == 0 {
		if len(resultValues) != 0 {
			r.errf("BASHPP-EDECO-RESULT: %s declares no results; decorator supplied %d\n", fn.name(), len(resultValues))
			r.exit.code = 1
			return nil, false
		}
		if fn.goError {
			cell, text := bashPPGoErrorCell(name, chain.call.Status)
			r.bashPPResultCells = []*bashPPCell{cell}
			return []string{text}, true
		}
		return nil, true
	}
	if len(resultValues) == 0 {
		// A skipped body yields zero results.
		resultValues = make([]any, count)
		resultTypes := bashppResultTypeExprs(fn.bodyResults())
		for i := range resultValues {
			if i < len(resultTypes) {
				zero, meta := r.bashPPZeroValue(resultTypes[i])
				cell := &bashPPCell{declType: resultTypes[i]}
				bashPPStoreCellValue(cell, zero, meta)
				cell.typeName = bashPPNamedTypeBase(resultTypes[i])
				resultValues[i] = cell
			}
		}
	}
	if len(resultValues) != count {
		r.errf("BASHPP-EDECO-RESULT: %s declares %d result(s); decorator supplied %d\n", fn.name(), count, len(resultValues))
		r.exit.code = 1
		return nil, false
	}
	resultTypes := bashppResultTypeExprs(fn.bodyResults())
	results := make([]string, count)
	r.bashPPResultCells = make([]*bashPPCell, count)
	for i, value := range resultValues {
		if i >= len(resultTypes) || resultTypes[i] == nil {
			continue
		}
		cell, text := bashPPDecoratorValueCell(value)
		if _, typed := value.(*bashPPCell); !typed && !r.bashPPValueFits(bashPPTypeText(resultTypes[i]), text) {
			r.errf("BASHPP-EDECO-RESULT: %s: cannot use %q as %s result %d\n", fn.name(), text, bashPPTypeText(resultTypes[i]), i+1)
			r.exit.code = 1
			return nil, false
		}
		converted, err := r.goSourceExpectedCell(cell, resultTypes[i])
		if err != nil {
			r.errf("BASHPP-EDECO-RESULT: %s: cannot use %q as %s result %d\n", fn.name(), text, bashPPTypeText(resultTypes[i]), i+1)
			r.exit.code = 1
			return nil, false
		}
		// Conversion to the declared result type may allocate a fresh cell.
		// Channel identity is runtime provenance rather than its printable
		// value, so carry it across just as parameter binding does.
		converted.channel, converted.channelOwner = cell.channel, cell.channelOwner
		converted.declType = resultTypes[i]
		r.bashPPResultCells[i] = converted
		results[i] = text
		if i < len(resultNames) && resultNames[i] != "" {
			if target := r.bashPPScope.lookup(resultNames[i]); target != nil {
				target.publish(bashPPCopyAssignmentCell(converted))
			} else {
				r.setVarString(resultNames[i], text)
			}
		}
	}
	if fn.goError {
		cell, text := bashPPGoErrorCell(name, chain.call.Status)
		results = append(results, text)
		r.bashPPResultCells = append(r.bashPPResultCells, cell)
	}
	// Results were settled by the chain, so the frame's own return state
	// must not settle them again.
	r.bashPPReturn = bashPPReturnState{}
	return results, true
}

// bashPPCallDecorated runs a shell function's body through its decorator
// chain, positioned exactly where Runner.call would run the body.
func (r *Runner) bashPPCallDecorated(ctx context.Context, name string, entry *bashPPDecorated, args []string, run func(context.Context)) {
	rungs := bashPPDecoratorRungs(entry.decorators, entry.advised)
	chain := r.bashPPNewDecoratorChain(name, rungs, args, nil, r.bashPPAgenticFunc(name))
	defer chain.release()
	chain.body = func(ctx context.Context) (int, []string) {
		// The body's "$@" is fed from the context afresh on every Next, so a
		// decorator's Args rewrite reaches the positional parameters. A shell
		// function has no declared arity or types to revalidate.
		fresh := make([]string, len(chain.call.Args))
		for i, value := range chain.call.Args {
			fresh[i] = bashPPDecoratorValueText(value)
		}
		r.Params = fresh
		run(ctx)
		if !r.exit.exiting && !r.bashPPPanicking() {
			r.exit.returning = false
		}
		return int(r.exit.code), nil
	}
	ok := chain.run(ctx)
	if !ok || r.exit.exiting || r.bashPPPanicking() {
		return
	}
	r.exit = exitStatus{code: uint8(chain.call.Status)}
}

// bashPPPrintDecorators writes a function's source decorators in the shape
// `declare -f` prints them, one per line above the declaration. Advised
// decorators are policy, not source, and are never printed.
func (r *Runner) bashPPPrintDecorators(decorators []*syntax.BashPPDecorator) {
	for _, d := range decorators {
		var buf strings.Builder
		buf.WriteString("@")
		buf.WriteString(d.Name.Value)
		buf.WriteString("(")
		positional := len(d.Args) - len(d.ArgNames)
		for i, arg := range d.Args {
			if i > 0 {
				buf.WriteString(", ")
			}
			if i >= positional {
				buf.WriteString(d.ArgNames[i-positional].Value)
				buf.WriteString(": ")
			}
			var w strings.Builder
			syntax.NewPrinter().Print(&w, arg)
			buf.WriteString(w.String())
		}
		buf.WriteString(")\n")
		r.out(buf.String())
	}
}
