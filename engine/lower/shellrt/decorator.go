package shellrt

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"
)

// This file carries the Bash++ decorator contract (see
// dhnt/docs/bashpp-decorators-and-advice.md §2 and §5) for generated programs.
// A decorated typed callable's private entry builds one Call, hands it to
// Decorate with its rungs and its body, and reads Status and Results back.
// The public symbol keeps its declared signature, and every indirect route —
// method value, interface dispatch, function handle — already reaches the
// private entry, so nothing can bypass the chain.
//
// Call is the decorator context: the one value every decorator receives.
// A decorator never sees or changes the target's signature; it observes the
// call through this context and decides whether the rest of the chain runs by
// calling Next. Skipping Next skips the body: the target then yields zero
// results and whatever Status the decorator left behind.
type Call struct {
	// Name is the declared name of the decorated callable; a method is
	// spelled Type.Method.
	Name string
	// Site is the call site the private entry was reached from, and Caller
	// the callable it was made from ("main" at the top level).
	Site   string
	Caller string
	// Args are the bound parameter values, one entry per position; a
	// variadic tail contributes one entry per element. A structured value —
	// a channel, a map, a pointer, a function handle — is the value itself,
	// so its identity survives the chain unless a decorator replaces it.
	Args []any
	// Results are the target's result values once Next has run. A decorator
	// may rewrite them; they are type-checked back into the declared results
	// when the chain completes.
	Results []any
	// Status is the exit status the call will report.
	Status int
	// Agentic reports whether the DECLARATION is marked agentic.
	Agentic bool
	// Advised is reserved for policy advice, which compiled programs do not
	// carry yet; it is always empty here.
	Advised string

	chain *decoratorChain
}

// Next runs the next decorator in the chain, or the body when this is the
// innermost decorator. Native decorators may pass a derived context; it
// applies only to that continuation, without mutating the caller Program.
// Next may be called more than once — a retry decorator
// does exactly that — and each call re-runs everything inside it.
func (c *Call) Next(contexts ...context.Context) {
	if c != nil && c.chain != nil {
		p := c.chain.p
		if len(contexts) > 0 && contexts[0] != nil {
			derived := *p
			derived.Context = p.Frame.Context(contexts[0])
			p = &derived
		}
		c.chain.next(p)
	}
}

// Run evaluates src as shell source in the program's session with the
// call's current Args as $1..$n and vars bound as shell variables, inside a
// subshell so nothing src assigns leaks back — the compiled counterpart of
// the interpreter's Call.Run. The lexical (typed) bindings visible to a
// shell region are visible to src, and ctx governs its commands exactly as
// it governs the body's (an effect cap rides it). It returns src's exit
// status; a session or parse fault is status 2.
func (c *Call) Run(ctx context.Context, src string, vars map[string]string) int {
	if c == nil || c.chain == nil || c.chain.p == nil || c.chain.p.Session == nil {
		return 1
	}
	p := c.chain.p
	var b strings.Builder
	b.WriteString("(\nset --")
	for _, arg := range c.Args {
		b.WriteString(" ")
		b.WriteString(shellQuote(DecoratorArgText(arg)))
	}
	names := make([]string, 0, len(vars))
	for name := range vars {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		b.WriteString("\n" + name + "=" + shellQuote(vars[name]))
	}
	b.WriteString("\n" + src + "\n)")
	exchange, err := p.shellExchangeBindings().BeginShell(p.Session)
	if err != nil {
		return 2
	}
	defer exchange.EndShell(p.Session)
	if ctx == nil {
		ctx = p.Context
	}
	ctx, _ = WithLexicalDeclarations(ctx, p.Bindings)
	if err := p.Session.Shell(ctx, b.String()); err != nil {
		return 2
	}
	return p.Session.Status()
}

// shellQuote single-quotes s for the shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// DecoratorArg is one evaluated argument of a decorator line handed to a
// native decorator, positional when Name is empty.
type DecoratorArg struct {
	Name  string
	Value string
}

// DecoratorFunc is a native decorator. A non-nil error aborts the call with
// status 1.
type DecoratorFunc func(ctx context.Context, c *Call, args []DecoratorArg) error

// Decorators is the process-level native decorator registry, wiring in the
// style of Adapter: set once at startup by the embedder, consulted by name
// when a generated chain reaches a rung that no source function defines. A
// compiled program runs with no wiring at all; a rung that resolves nowhere is
// EDECO-UNDEF at call time, exactly as in the interpreter.
var Decorators map[string]DecoratorFunc

// Decorator is one rung of a generated chain, outermost first. A source
// decorator sets Run, which the compiler binds to the decorator's private
// entry with the rung's arguments evaluated per invocation. A rung with a nil
// Run resolves Name through Decorators when it runs, with Args evaluated then.
type Decorator struct {
	Name string
	Run  func(p *Program, c *Call)
	Args func(p *Program) []DecoratorArg
}

// DecoratorArgText renders one native decorator argument the way the
// interpreter hands a native decorator its evaluated argument words.
func DecoratorArgText(value any) string {
	return fmt.Sprint(value)
}

type decoratorChain struct {
	p      *Program
	call   *Call
	rungs  []Decorator
	body   func(*Program) error
	depth  int
	failed bool
}

// Decorate runs one decorated invocation: the rungs outermost first, each
// Next reaching the next rung and finally body. body rebinds the target's
// parameters from the context, runs the target and records its results; the
// error it returns is a binding diagnostic that fails the chain. Decorate
// reports whether the chain completed; on completion the program's status is
// the context's Status and Results hold the outcome for the entry to settle.
func (p *Program) Decorate(c *Call, rungs []Decorator, body func(*Program) error) bool {
	chain := &decoratorChain{p: p, call: c, rungs: rungs, body: body}
	c.chain = chain
	chain.next(p)
	c.chain = nil
	if chain.failed {
		return false
	}
	// A shell exit status is 8-bit: `$?` wraps a decorator-set Status the same
	// way the interpreter does (interp truncates the settled status to uint8),
	// so an out-of-range c.Status normalizes identically in both engines and
	// stays consistent with the @go.error message minted from the same value.
	p.SetStatus(NormalStatus(c.Status))
	return true
}

// NormalStatus wraps a status into the 8-bit range a shell `$?` reports, so a
// decorator-supplied Status matches the interpreter's uint8 truncation.
func NormalStatus(status int) int { return int(uint8(status)) }

func (c *decoratorChain) next(p *Program) {
	if c.failed {
		return
	}
	previous := c.p
	c.p = p
	defer func() { c.p = previous }()
	depth := c.depth
	c.depth++
	defer func() { c.depth = depth }()
	if depth >= len(c.rungs) {
		c.runBody()
		return
	}
	rung := c.rungs[depth]
	if rung.Run != nil {
		rung.Run(p, c.call)
		return
	}
	native := Decorators[rung.Name]
	if native == nil && rung.Name == timedDecoratorName {
		native = timedDecorator(c.p)
	}
	if native == nil {
		c.fail(fmt.Sprintf("BASHPP-EDECO-UNDEF: decorator %s is not defined", rung.Name))
		return
	}
	var args []DecoratorArg
	if rung.Args != nil {
		args = rung.Args(p)
	}
	if err := native(c.p.Context, c.call, args); err != nil {
		c.fail(fmt.Sprintf("BASHPP-EDECO-NATIVE: @%s: %v", rung.Name, err))
	}
}

// runBody is one invocation of the target. The body closure is an ordinary
// Go call boundary, so its defers finish before Next returns to the innermost
// decorator, and a repeated Next is an independent invocation. The status the
// body left is captured on the context and cleared, as the engine does.
func (c *decoratorChain) runBody() {
	if err := c.body(c.p); err != nil {
		c.fail(err.Error())
		return
	}
	c.call.Status = c.p.Status()
	c.p.SetStatus(0)
}

// fail records a decorator diagnostic with status 1 and marks the chain
// failed; every enclosing Next then returns without running anything else.
func (c *decoratorChain) fail(msg string) {
	c.failed = true
	c.p.Fail(&DecoratorError{Msg: msg})
}

// DecoratorError is a decorator chain diagnostic. Its message is the
// engine's, and Fail reports it at status 1.
type DecoratorError struct{ Msg string }

func (e *DecoratorError) Error() string { return e.Msg }

// DecoratedArity is the body's arity check before rebinding: a decorator's
// Args rewrite may have broken what the original call proved.
func DecoratedArity(c *Call, name string, fixed int, variadic bool) error {
	switch {
	case variadic && len(c.Args) < fixed:
		return &DecoratorError{Msg: fmt.Sprintf("BASHPP-EDECO-ARG: %s: decorator supplied %d argument(s); expected at least %d", name, len(c.Args), fixed)}
	case !variadic && len(c.Args) != fixed:
		return &DecoratorError{Msg: fmt.Sprintf("BASHPP-EDECO-ARG: %s: decorator supplied %d argument(s); expected %d", name, len(c.Args), fixed)}
	}
	return nil
}

// DecoratedArg rebinds one parameter from the context. T is the declared
// parameter type; a nil entry binds T's zero value, as a nil interface,
// pointer, channel, map or handle argument would.
func DecoratedArg[T any](c *Call, name string, index int, param string) (T, error) {
	var zero T
	if index < 0 || index >= len(c.Args) {
		return zero, &DecoratorError{Msg: fmt.Sprintf("BASHPP-EDECO-ARG: %s: decorator supplied no argument for parameter %s", name, param)}
	}
	value, ok := decoratedValue[T](c.Args[index])
	if !ok {
		return zero, &DecoratorError{Msg: fmt.Sprintf("BASHPP-EDECO-ARG: %s: cannot use %q as %s value for parameter %s", name, DecoratorArgText(c.Args[index]), valueTypeName(reflect.TypeFor[T]()), param)}
	}
	return value, nil
}

// DecoratedVariadic rebinds the variadic tail from the context, one element
// per entry from index on.
func DecoratedVariadic[T any](c *Call, name string, index int, param string) ([]T, error) {
	if index > len(c.Args) {
		return nil, &DecoratorError{Msg: fmt.Sprintf("BASHPP-EDECO-ARG: %s: decorator supplied no argument for parameter %s", name, param)}
	}
	tail := make([]T, 0, len(c.Args)-index)
	for i := index; i < len(c.Args); i++ {
		value, ok := decoratedValue[T](c.Args[i])
		if !ok {
			return nil, &DecoratorError{Msg: fmt.Sprintf("BASHPP-EDECO-ARG: %s: cannot use %q as %s value for parameter %s", name, DecoratorArgText(c.Args[i]), valueTypeName(reflect.TypeFor[T]()), param)}
		}
		tail = append(tail, value)
	}
	return tail, nil
}

// DecoratedResults validates the chain's result count against the declared
// one. A skipped body yields no results at all, which settles to the declared
// zero values; anything else must match the declaration exactly.
func (p *Program) DecoratedResults(c *Call, name string, count int) bool {
	switch {
	case len(c.Results) == 0 && count == 0:
		return true
	case count == 0:
		p.Fail(&DecoratorError{Msg: fmt.Sprintf("BASHPP-EDECO-RESULT: %s declares no results; decorator supplied %d", name, len(c.Results))})
		return false
	case len(c.Results) == 0:
		c.Results = make([]any, count)
		return true
	case len(c.Results) != count:
		p.Fail(&DecoratorError{Msg: fmt.Sprintf("BASHPP-EDECO-RESULT: %s declares %d result(s); decorator supplied %d", name, count, len(c.Results))})
		return false
	}
	return true
}

// DecoratedResult settles one result from the context into its declared
// type T. A nil entry is the declared zero, which is what a skipped body
// yields; a value of another type is EDECO-RESULT.
func DecoratedResult[T any](p *Program, c *Call, name string, index int) (T, bool) {
	var zero T
	if index < 0 || index >= len(c.Results) {
		p.Fail(&DecoratorError{Msg: fmt.Sprintf("BASHPP-EDECO-RESULT: %s: result %d was never produced", name, index+1)})
		return zero, false
	}
	value := c.Results[index]
	if value == nil {
		return zero, true
	}
	typed, ok := value.(T)
	if !ok {
		p.Fail(&DecoratorError{Msg: fmt.Sprintf("BASHPP-EDECO-RESULT: %s: cannot use %q as %s result %d", name, DecoratorArgText(value), valueTypeName(reflect.TypeFor[T]()), index+1)})
		return zero, false
	}
	return typed, true
}

// decoratedValue converts one context entry to the declared type T: a nil
// entry is T's zero and anything else must already be a T. There is no
// conversion step — the context holds the very Go values the parameters and
// results are declared with, so an assertion is the whole check.
func decoratedValue[T any](value any) (T, bool) {
	var zero T
	if value == nil {
		return zero, true
	}
	typed, ok := value.(T)
	return typed, ok
}
