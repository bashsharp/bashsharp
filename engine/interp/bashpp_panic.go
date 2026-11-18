// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"fmt"
	"strings"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Go-compatible `panic` and `recover` for the sequential Bash++ subset.
//
// A PANIC IS NOT AN EXIT STATUS. The distinction this file exists to keep is
// that a panic is a *control transfer*, not a failed command: the statements
// after the panic site never run, each active frame is abandoned rather than
// returned from, and every frame's deferred calls still run as it is abandoned.
// A non-zero status would give none of that, which is why the panic lives in
// its own runner state instead of being encoded into [exitStatus].
//
// HOW IT UNWINDS. There is no host-level Go panic here. Unwinding reuses the
// halt the interpreter already has: [Runner.stop] reports a live panic, so no
// further statement runs anywhere, at any depth, no matter which boundary
// cleared the `returning` flag on the way out. The flag is still set alongside
// it, because that is what makes a loop *break* rather than spin through
// no-op iterations. Every frame boundary then asks one question — am I still
// panicking? — and either keeps unwinding or resumes normally.
//
// WHERE IT STOPS. Three ways, and only three:
//
//  1. A deferred call recovers it. The frame whose defers are running returns
//     normally, with its named results as the deferred call left them, and the
//     caller cannot tell a panic happened. See [Runner.bashPPRecover].
//  2. It escapes the outermost Go-form frame. Nothing further can recover it,
//     so it is reported and the shell terminates with status 2, as an
//     unrecovered Go panic terminates a program. See
//     [Runner.bashPPPanicTerminate].
//  3. A deferred call runs an explicit `exit`. The script asked to terminate,
//     and it terminates with that status and no panic report — the same
//     precedence `os.Exit` has over a panic in flight.
//
// A subshell is a process boundary, so a panic never crosses one: the subshell
// carries no active panic in (it is not copied by [Runner.subshell]) and no
// Go-form frame either, so a panic raised inside one settles inside it, giving
// the subshell exit status 2 and leaving the parent to observe that status like
// any other.

// bashPPPanicStatus is the status of a shell terminated by an unrecovered
// panic. Go exits 2 for an unrecovered panic; so does this.
const bashPPPanicStatus = 2

// bashPPPanicState is the panic unwinding the Bash++ call stack.
//
// chain holds every value raised during ONE unwind, oldest first. A panic
// raised while another is unwinding becomes the active one — a recover takes
// the newest, and a report prints all unrecovered values. If that newest panic
// is recovered inside the deferred call that raised it, the older panic keeps
// unwinding; this is the recursive-panic rule exercised by Go's runtime tests.
type bashPPPanicState struct {
	active bool
	// running marks that a deferred call is executing FOR this panic. It is
	// the difference between "a panic exists" and "a panic is halting the
	// shell": the cleanups an unwind runs are ordinary statements and must be
	// allowed to run, while everything else must not. A panic raised inside a
	// cleanup clears it again, so the rest of that cleanup is abandoned too.
	// Frames deeper than the panic has unwound to are never halted by it;
	// see bashPPPanicHalts.
	running bool
	chain   []string
	values  []any
	// depths records, for each entry of chain, the call-stack depth the panic
	// was raised at, lowered to each frame it unwinds to as that frame runs
	// its deferred calls. It answers two questions: whether the executing
	// frame is the one being abandoned (bashPPPanicking), and which older
	// panics a recover discards along with the newest — every one raised by
	// the recovering frame or deeper, the panics a deferred call started
	// while that frame was already unwinding — so the frame returns normally
	// instead of resuming an unwind Go has abandoned.
	depths []int
	// GoSource panics retain the interpreted frame names and source site so the
	// process boundary reports a genuine Go-shaped traceback. Classic Bash++
	// keeps its established concise diagnostic.
	traceSource string
	traceLine   uint
	traceFrames []string
	// recovered is the payload the most recent successful recover took, kept
	// so a statement binding recover's result (`p := recover()`) can present
	// the typed interface value rather than its rendered text.
	recovered *bashPPInterfaceValue
}

// value is the payload a recover would take: the most recent panic's.
func (p bashPPPanicState) value() string {
	if len(p.chain) == 0 {
		return ""
	}
	return p.chain[len(p.chain)-1]
}

// bashPPPanicking reports whether a panic is abandoning the frame that is
// executing now: one raised by this frame or a deeper one, or one that has
// unwound to it. A panic an OUTER frame is unwinding is not — the deferred
// call that frame is running may call other functions, and those return
// normally; see bashPPPanicState.depths.
func (r *Runner) bashPPPanicking() bool {
	p := &r.bashPPPanic
	if !p.active {
		return false
	}
	return len(p.depths) == 0 || p.depths[len(p.depths)-1] >= len(r.callStack)
}

// bashPPPanicHalts reports whether a panic must stop the next statement from
// running: when it is abandoning the executing frame and no cleanup of that
// frame is in progress. A deferred Go-form call runs one frame deeper than
// the frame the panic has unwound to, so its statements — and those of
// anything it calls — run whatever the flag says; a deferred shell command
// runs in the unwinding frame itself and needs the lifted halt (running).
// A panic a cleanup raises is deeper again and halts it. See
// [bashPPPanicState.depths] and [bashPPPanicState.running].
func (r *Runner) bashPPPanicHalts() bool {
	return r.bashPPPanicking() && !r.bashPPPanic.running
}

// bashPPPredeclaredCall reports which predeclared Bash++ function a call names,
// or "" for any other callee.
//
// It is consulted only AFTER the session's own functions, so a script that
// declares `func panic(...)` shadows the predeclared one, exactly as a Go
// declaration shadows a predeclared identifier.
func bashPPPredeclaredCall(c *syntax.BashPPCall) string {
	if c == nil || c.FuncLit != nil || len(c.Fun) != 1 {
		return ""
	}
	switch name := c.Fun[0].Value; name {
	case "append", "cap", "clear", "copy", "delete", "len", "make",
		"max", "min", "new", "panic", "print", "println", "recover":
		return name
	}
	return ""
}

// bashPPPredeclared runs a predeclared call. The second result reports whether
// it produced VALUES a caller may bind: `recover` does, and `panic` never does,
// since a call that transfers control has nothing to return to a `:=`.
//
// The arguments arrive already evaluated, which is what makes `defer panic(v)`
// panic with the value v held when the defer ran rather than when the frame
// unwound.
//
// `recover` reports through the exit status as well as through its result,
// because the payload alone cannot answer the question Go answers with nil: a
// panic value may itself be the empty string. Status 0 means a panic was
// recovered, status 1 means there was nothing to recover.
func (r *Runner) bashPPPredeclared(name string, c *syntax.BashPPCall, args []string) ([]string, bool) {
	switch name {
	case "panic":
		if len(args) != 1 || c.Ellipsis.IsValid() {
			r.errf("panic: takes exactly one argument\n")
			r.exit = exitStatus{code: 2}
			return nil, false
		}
		r.bashPPPanicTrace(c)
		value, text, ok := r.bashPPPanicOperand(c, args[0])
		if !ok {
			return nil, false
		}
		r.bashPPRaiseValue(text, value)
		return nil, false
	case "recover":
		if len(args) != 0 {
			r.errf("recover: takes no arguments\n")
			r.exit = exitStatus{code: 2}
			return nil, false
		}
		value, ok := r.bashPPRecover()
		r.bashPPPanic.recovered = nil
		if ok && r.bashPPGoSource {
			r.bashPPPanic.recovered = bashPPBoxPanicValue(value)
		}
		r.exit = exitStatus{}
		r.exit.oneIf(!ok)
		// "nothing to recover" is this call's ANSWER, not a failed command:
		// recover reports through the status because a panic value may itself
		// be the empty string. Go source form aborts on a statement that
		// reports a non-zero status, so without the exemption
		// `defer func() { recover() }()` — the guard the standard library
		// writes when it only cares that a call panicked — would terminate the
		// very frame it exists to let continue.
		//
		// Stamp the status with this call's provenance as well. The exemption
		// alone is shared with the errexit contexts in [Runner.stmts]; only a
		// stamp identifies the status THIS call reported, which is what a
		// hosted test callback may reduce and nothing else is.
		if !ok {
			r.bashPPRecoverSeq++
			r.exit.recoverSeq = r.bashPPRecoverSeq
		}
		r.exit.errexitExempt = true
		// The shell-facing view of recover's result: a nil interface (no panic
		// in flight) is the empty string, exactly as the compiled lowering
		// renders it; only a real payload is printed with fmt.Sprint.
		text := ""
		if value != nil {
			text = fmt.Sprint(value)
		}
		return []string{text}, true
	}
	return nil, false
}

// bashPPPanicOperand evaluates the value `panic(v)` receives and the text its
// report prints, from the call's positioned operand where there is one. It
// reports false when the evaluation itself stopped the statement — an
// interrupted or refused dependency operand — and there is nothing to raise.
//
// The direct call raises what this conversion returns at once. A launched
// panic supplies a retained operand binding, so inspecting its type here never
// re-evaluates user code (see gosource_task_native.go).
func (r *Runner) bashPPPanicOperand(c *syntax.BashPPCall, arg string) (value any, text string, ok bool) {
	value = arg
	if r.bashPPGoSource && len(c.ArgExprs) == 1 {
		if cell, err := r.bashPPStructuredArgCell(c.Args[0], c.ArgExprs[0]); err == nil && cell != nil {
			if cell.interfaceValue != nil {
				value = cell.interfaceValue
			} else if boxed := r.goSourcePanicStructuredValue(cell); boxed != nil {
				value = boxed
			}
		} else if boxed, text, ok := r.goSourcePanicNativeValue(c.ArgExprs[0]); ok {
			if boxed == nil {
				return nil, "", false
			}
			return boxed, text, true
		}
		if scalar, err := r.bashPPEvalScalarExpr(c.ArgExprs[0]); err == nil {
			if _, alreadyInterface := value.(*bashPPInterfaceValue); !alreadyInterface {
				value = r.bashPPPanicScalarValue(scalar)
			}
		}
	}
	text = arg
	if r.bashPPGoSource {
		value, text = r.bashPPPanicArgument(c, value, text)
	}
	return value, text, true
}

// bashPPRaise starts a panic with value, abandoning the current statement.
//
// With no Go-form frame active there is nothing to unwind and no defer that
// could recover it, so the panic is reported and terminates the shell at once
// rather than pretending to look for a handler that cannot exist.
func (r *Runner) bashPPRaise(value string) {
	// A runtime fault raised by its message alone still panics with the
	// runtime's error value in an original Go program; see
	// bashpp_sprint165_runtime_panic.go.
	if r.bashPPGoSource {
		if payload, ok := bashPPRuntimeErrorPayload(value); ok {
			r.bashPPRaiseValue(value, payload)
			return
		}
	}
	r.bashPPRaiseValue(value, value)
}

func (r *Runner) bashPPRaiseValue(text string, value any) {
	r.bashPPPanic.chain = append(r.bashPPPanic.chain, text)
	r.bashPPPanic.values = append(r.bashPPPanic.values, value)
	r.bashPPPanic.depths = append(r.bashPPPanic.depths, len(r.callStack))
	r.bashPPPanic.active = true
	// A panic raised inside a cleanup is a new unwind, not a continuation of
	// the one that ran the cleanup: it abandons the rest of that cleanup too.
	r.bashPPPanic.running = false
	r.goSourceCaptureFault()
	if r.bashPPFuncActive == 0 {
		r.bashPPPanicTerminate()
		return
	}
	r.bashPPUnwind()
}

// bashPPUnwind puts the runner into the halted state a panic needs: no further
// statement runs, and every loop and compound body treats the frame as leaving.
func (r *Runner) bashPPUnwind() {
	r.exit = exitStatus{code: bashPPPanicStatus, returning: true}
}

// bashPPRecover implements Go's recover.
//
// It succeeds only in a call the unwinding frame deferred DIRECTLY: the call
// stack must be exactly one frame deeper than the frame whose defers are
// running. That single comparison covers every case the spec lists — a recover
// in the function body itself is too shallow, a recover in a function called
// BY the deferred function is too deep, and `defer recover()` never pushes a
// frame at all, so it is too shallow as well and does not stop the panic.
func (r *Runner) bashPPRecover() (any, bool) {
	if !r.bashPPPanic.active {
		return nil, false
	}
	// runtime.Goexit is not a panic: recover returns nil and the goroutine
	// keeps ending. See bashpp_goexit.go.
	if r.bashPPGoexiting() {
		return nil, false
	}
	if r.bashPPDeferDepth == 0 || len(r.callStack) != r.bashPPDeferDepth {
		return nil, false
	}
	last := len(r.bashPPPanic.chain) - 1
	// Only the panic the deferring frame is unwinding is visible: one an
	// outer frame is still unwinding — after this cleanup recovered the
	// nested panic that interrupted it — belongs to that frame's own
	// deferred calls. See bashPPPanicState.depths.
	if last < len(r.bashPPPanic.depths) && r.bashPPPanic.depths[last] != r.bashPPDeferDepth-1 {
		return nil, false
	}
	value := r.bashPPPanic.chain[last]
	r.bashPPPanic.chain = r.bashPPPanic.chain[:last]
	payload := r.bashPPPanic.values[last]
	r.bashPPPanic.values = r.bashPPPanic.values[:last]
	r.bashPPPanic.depths = r.bashPPPanic.depths[:last]
	// The recovering frame is the one whose deferred call this is. Every
	// older panic it or a deeper frame raised was aborted by the one just
	// recovered and is discarded with it; only a panic an outer frame is
	// still unwinding stays suspended. See bashPPPanicState.depths.
	frame := r.bashPPDeferDepth - 1
	keep := len(r.bashPPPanic.chain)
	for keep > 0 && r.bashPPPanic.depths[keep-1] >= frame {
		keep--
	}
	r.bashPPPanic.chain = r.bashPPPanic.chain[:keep]
	r.bashPPPanic.values = r.bashPPPanic.values[:keep]
	r.bashPPPanic.depths = r.bashPPPanic.depths[:keep]
	r.bashPPPanic.active = len(r.bashPPPanic.chain) > 0
	// A directly deferred invocation continues after recover. When it has
	// recovered a nested panic, an older panic still exists but stays suspended
	// until this cleanup returns to the older unwind's defer runner.
	r.bashPPPanic.running = r.bashPPPanic.active
	r.goSourceFaultRecovered()
	_ = value
	return payload, true
}

// bashPPPanicTerminate reports an unrecovered panic and terminates the shell.
//
// Classic Bash++ reports only the panic chain. GoSource also reports the real
// interpreted function frames and panic site it retained at the raise point.
func (r *Runner) bashPPPanicTerminate() {
	if r.bashPPGoexiting() {
		// The Goexit left the outermost interpreted frame with its deferred
		// calls run. A goroutine simply ends; the main goroutine ending this
		// way leaves the program with no goroutine able to finish it.
		r.bashPPPanic = bashPPPanicState{}
		if r.bashPPGoTask {
			r.exit = exitStatus{}
			return
		}
		r.bashPPMainGoexit = true
		if c := r.bashPPConcurrent; c != nil {
			c.mu.Lock()
			c.mainGoexit = true
			c.changed.Broadcast()
			c.mu.Unlock()
		}
		r.exit = exitStatus{exiting: true}
		return
	}
	var b strings.Builder
	for i, value := range r.bashPPPanic.chain {
		if i > 0 {
			b.WriteString("\t")
		}
		b.WriteString("panic: ")
		b.WriteString(value)
		b.WriteString("\n")
	}
	if r.bashPPGoSource {
		// Go's GOTRACEBACK=none retains the panic value and status but omits
		// the traceback. Honour it here too: unlike a post-processing filter,
		// this is the program's selected runtime output contract.
		if r.lookupVar("GOTRACEBACK").String() != "none" {
			b.WriteString("\ngoroutine 1 [running]:\n")
			for i := len(r.bashPPPanic.traceFrames) - 1; i >= 0; i-- {
				name := r.bashPPPanic.traceFrames[i]
				if name == "" {
					name = "func1"
				}
				b.WriteString("main.")
				b.WriteString(name)
				b.WriteString("()\n")
				if i == len(r.bashPPPanic.traceFrames)-1 && r.bashPPPanic.traceSource != "" {
					fmt.Fprintf(&b, "\t%s:%d\n", r.bashPPPanic.traceSource, r.bashPPPanic.traceLine)
				}
			}
		}
	}
	r.errf("%s", b.String())
	r.bashPPPanic = bashPPPanicState{}
	r.exit = exitStatus{code: bashPPPanicStatus, exiting: true}
}

// bashPPPanicSettled reports whether a hard exit has overtaken the panic, and
// discards it when so.
//
// An explicit `exit` from a deferred call is the script terminating itself on
// purpose. That outranks a panic in flight, and it suppresses the panic report
// for the same reason `os.Exit` does: the program is not crashing, it is
// leaving.
func (r *Runner) bashPPPanicSettledByExit() bool {
	if !r.bashPPPanic.active || !(r.exit.exiting || r.exit.fatalExit) {
		return false
	}
	r.bashPPPanic = bashPPPanicState{}
	return true
}
