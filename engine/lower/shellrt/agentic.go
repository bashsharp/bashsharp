package shellrt

import (
	"context"
	"fmt"
)

// This file carries the Bash++ agentic assistance contract (see
// docs/bashpp-agentic.md) for generated programs. The contract is a source
// opt-in: an explicit block permits assistance for the statements it encloses,
// and a marked callable requires an agentic caller. Nothing here selects a
// model, opens a network connection, spends anything, or grants a permission.
//
// The state is an explicit immutable value threaded through generated code.
// There is deliberately no goroutine identity, no thread-local, and no mutable
// package-level assistance scope: a copied Frame is an independent observation,
// which is what makes subshells, pipelines, tasks and deferred calls safe
// without any save/restore bookkeeping.

// Frame is the immutable agentic state of one execution region. The zero Frame
// has assistance off, so a region that never opted in cannot accidentally
// inherit it. Every transition returns a new Frame; no method mutates its
// receiver, and a Frame may be copied across goroutines.
type Frame struct {
	on bool
}

// Off returns the assistance-off frame. It is the zero Frame, named so that
// generated code states the rule it is applying.
func Off() Frame { return Frame{} }

// Source returns the entry frame of a compiled unit from its source opt-in
// boolean. The boolean comes from the compiled source alone; no environment
// variable, flag or ambient runtime state supplies it.
func Source(optIn bool) Frame { return Frame{on: optIn} }

// Callback returns the entry frame of a trap callback, including signal traps,
// and of a mapfile/readarray -C callback. Callbacks start with assistance off
// and may opt in with their own explicit block; the interrupted caller keeps
// its own frame, which is simply the value it never handed over.
func Callback() Frame { return Frame{} }

// File returns the entry frame of a sourced script or a new file run. Both
// start with assistance off regardless of the frame that reached them.
func File() Frame { return Frame{} }

// Agentic reports whether assistance is permitted for the statements running
// in this frame. It is the observation a cooperating in-process tool reads; it
// does not imply that any request will be made.
func (f Frame) Agentic() bool { return f.on }

// Block returns the frame for the statements of an explicit agentic block. The
// block runs in the current shell, creates no variable scope, and opts in
// unconditionally, so the enclosing frame does not affect the result.
func (f Frame) Block() Frame { return Frame{on: true} }

// Eval returns the frame for eval'd text, which uses its current frame.
func (f Frame) Eval() Frame { return f }

// Child returns the independent copy taken by a subshell, pipeline element or
// task. The copy is independent because Frame is immutable: nothing the child
// does can be observed by the parent frame.
func (f Frame) Child() Frame { return f }

// Defer returns the frame captured when a deferred call is scheduled. A
// deferred call retains this value, so it runs in its scheduling frame even
// though the enclosing region has already been left.
func (f Frame) Defer() Frame { return f }

// Enter is the callable entry check, and is the whole of the "requires an
// agentic caller" rule. Generated code must call it before emitting any of the
// callable's body: a marked callable reached from a frame with assistance off
// fails here, and its body never runs.
//
// marked is the callable's declaration marker, resolved by the compiler. The
// returned body frame is derived from the declaration, not inherited from the
// caller: an ordinary helper or closure called inside an agentic block runs
// with assistance off until it enters its own explicit block.
func (f Frame) Enter(site Site, marked bool) (Frame, error) {
	if marked && !f.on {
		return Frame{}, &PermissionError{Site: site}
	}
	return Frame{on: marked}, nil
}

// Site is the position a denial is reported at. Name is the callable as
// written in the source. File is the origin file, and Line is the caller's
// statement line; a standalone entry, which has no in-program call site, leaves
// Line zero. See PermissionError.Error for the two resulting spellings.
type Site struct {
	Name string
	File string
	Line int
	// CallSite carries source metadata for decorators independently of the
	// diagnostic location, so enabling decorators does not change errors.
	CallSite string
}

// prefix reproduces the engine's diagnostic prefix. With a caller statement
// line this is bash's "file: line N: " form, defaulting the name to "bash"
// exactly as the engine does for an unnamed input. A standalone entry has no
// caller statement to report, so it uses the engine's unprefixed form rather
// than inventing a position spelling bash never produces.
func (s Site) prefix() string {
	if s.Line <= 0 {
		return ""
	}
	name := s.File
	if name == "" {
		name = "bash"
	}
	return fmt.Sprintf("%s: line %d: ", name, s.Line)
}

// PermissionError reports a marked callable reached without an agentic caller.
// Its message is the engine's, byte for byte, so an interpreted and a compiled
// run of the same source are diagnosed identically. Reporting it through Fail
// also matches the engine's status 1.
type PermissionError struct {
	Site Site
}

func (e *PermissionError) Error() string {
	return e.Site.prefix() + e.Site.Name + ": agentic action requires an explicit agentic { ...; } scope"
}

// agenticKey carries the observation on a context derived by Frame.Context.
type agenticKey struct{}

// Adapter, when set by the embedder before the generated program runs, derives
// the context that cooperating in-process tools observe. It is process-level
// wiring in the style of Stdout and Stderr — set once at startup, never per
// region — and is not itself agentic state: the frame remains the only
// authority for the boolean it is handed.
var Adapter func(ctx context.Context, agentic bool) context.Context

// Context returns the context a cooperating in-process tool is called with.
// The observation is always recorded under this package's own key, so Agentic
// works with no wiring at all; Adapter then gets to derive a further context,
// which is the seam an embedder uses to reach a tool that reads a different
// key. A nil result from Adapter leaves the recorded context in place.
func (f Frame) Context(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = context.WithValue(ctx, agenticKey{}, f.on)
	if Adapter != nil {
		if derived := Adapter(ctx, f.on); derived != nil {
			ctx = derived
		}
	}
	return ctx
}

// Agentic reports the observation carried by ctx, mirroring the engine's
// interp.HandlerCtx(ctx).Agentic for tools reached from generated Go. A
// context that never passed through Frame.Context reports false.
func Agentic(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	on, _ := ctx.Value(agenticKey{}).(bool)
	return on
}
