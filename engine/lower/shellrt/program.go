package shellrt

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
)

// Program is the runtime state a generated program threads explicitly through
// every region it emits: the context an in-process tool observes, the session
// that owns status, streams, shell state and tasks, the channel ownership
// scope, the readonly guard state, and the immutable agentic frame.
//
// There is deliberately no package-level status, stdout or failure sink on
// this path. Two programs in one process — a test running artifacts in
// parallel, an embedder hosting several units — share nothing but what they
// were explicitly handed.
//
// A Program is produced by [NewProgram], [Program.Enter], [Program.Block] or
// [Program.Child]; the zero value is not usable. Every derivation returns a
// new *Program and leaves the receiver alone, which is what makes a frame,
// a region and a task independent observations rather than saved-and-restored
// global state.
type Program struct {
	// Bindings owns this entry's native lexical cells and captured name view.
	Bindings *LexicalBindings

	// Results is an explicit caller-owned invocation descriptor.
	Results   *ResultFrame
	Arguments *ResultFrame
	// ResultSidecars binds retained authority to actual native storage.
	ResultSidecars *ResultSidecars

	// Context is the context an in-process tool is called with. It is derived
	// from the frame in effect, so the agentic observation and the region
	// always agree.
	Context context.Context

	// Session owns the status, the standard streams, persistent shell state
	// and every task launched from this program.
	Session *Session

	// Channels is the channel ownership scope. It is shared by identity with
	// every derived program, including tasks: only the owning [Program.Run]
	// revokes it.
	Channels *ChannelScope

	// Readonly is the readonly guard state. It is safe for concurrent use, so
	// it is shared by identity with tasks. Remapping the marks of a cloned
	// object is an explicit compiler obligation, not something the runtime
	// infers.
	Readonly *ReadonlyState

	// Frame is the immutable agentic state of this region. Assigning it on a
	// freshly constructed root is how a compiled unit states its source
	// opt-in; every other transition goes through Enter or Block.
	Frame Frame

	// seq is the bookkeeping shared by the sequential regions of one
	// execution: the panic chain. A task forks it, because Go's panic state
	// is per goroutine.
	seq                *sequential
	nativeShells       *nativeShellRegistry
	nativeContinuation *NativeContinuation

	// owner marks the program whose Run revokes the channel scope. A task
	// entry is not an owner: it must not close channels its siblings still use.
	owner bool

	// callable and caller are the callable this region belongs to and the
	// one it was entered from, as Enter records them; both are empty at the
	// top level. They exist for the decorator context's Caller, which the
	// engine derives from its call stack.
	callable string
	caller   string
}

// sequential is the per-execution bookkeeping shared by Enter and Block and
// forked by Child.
type sequential struct {
	mu           sync.Mutex
	shortFailure uint64

	// panics is the active panic chain, oldest first. It mirrors what the
	// engine reports for a panic raised inside a panic; a recovered panic pops
	// its entry.
	panics []string

	// operationErr is a canceled operation whose source statement continues.
	// Sequential regions share it; tasks fork it with their own Program state.
	operationErr error
}

// NewProgram builds a program around a new session. The options are the
// session's: a unit with dynamic shell regions passes
// [WithShellFactory] so that this package never imports a shell backend, and
// a typed-only unit passes none and links no interpreter at all.
func NewProgram(opts ...SessionOption) (*Program, error) {
	session, err := NewSession(opts...)
	if err != nil {
		return nil, err
	}
	p := &Program{
		Bindings:       NewLexicalBindings(),
		ResultSidecars: NewResultSidecars(),
		Session:        session,
		Channels:       &ChannelScope{},
		Readonly:       &ReadonlyState{},
		Frame:          Off(),
		seq:            &sequential{},
		nativeShells:   &nativeShellRegistry{},
		owner:          true,
	}
	if continuation := session.nativeContinuation; continuation != nil {
		p.nativeContinuation = continuation
		p.nativeShells = &continuation.registry
	}
	p.Context = p.Frame.Context(session.Context())
	return p, nil
}

// derive returns the sequential region program for frame: the same session,
// channels, readonly state and panic bookkeeping, an immutable frame copy, and
// the context that frame is observed through.
func (p *Program) derive(frame Frame) *Program {
	derived := *p
	derived.Frame = frame
	derived.Context = frame.Context(p.Context)
	return &derived
}

// Enter is the callable entry check and the whole of the "requires an agentic
// caller" rule; see [Frame.Enter]. The marker is checked before anything else,
// so a denied call does no session, channel or provider work at all: the
// caller's prolog reports the error with [Program.Fail] and returns the
// callable's zero results without running the body.
//
// The body program shares the caller's sequential panic bookkeeping and its
// session, channel and readonly identities. Only the frame — and the context
// derived from it — differ.
func (p *Program) Enter(site Site, marked bool) (*Program, error) {
	frame, err := p.Frame.Enter(site, marked)
	if err != nil {
		return nil, err
	}
	entered := p.derive(frame)
	entered.callable, entered.caller = site.Name, p.callable
	return entered, nil
}

// Caller is the callable the current region was entered from, or "main" for
// a region the top level reached directly; it is the engine's spelling of the
// decorator context's Caller.
func (p *Program) Caller() string {
	if p.caller == "" {
		return "main"
	}
	return p.caller
}

// Block returns the program for the statements of an explicit agentic block.
// The block runs in the current shell and creates no scope of its own, so
// everything but the frame and its context is shared with the receiver.
func (p *Program) Block() *Program { return p.derive(p.Frame.Block()) }

// Child returns the program a task body runs against: the task's context and
// its own child session, so status and streams are the child's, with forked
// panic bookkeeping because a panic is per goroutine. The channel scope and
// the readonly state are retained by identity — a task shares its owner's
// channel authority and its readonly marks rather than a copy of them.
//
// A nil ctx or session falls back to the receiver's, so a caller that only
// wants the fork does not have to restate them.
func (p *Program) Child(ctx context.Context, session *Session) *Program {
	if session == nil {
		session = p.Session
	}
	if ctx == nil {
		ctx = session.Context()
	}
	frame := p.Frame.Child()
	return &Program{
		Bindings:       p.Bindings.Fork(),
		ResultSidecars: NewResultSidecars(),
		Context:        frame.Context(ctx),
		Session:        session,
		Channels:       p.Channels,
		Readonly:       p.Readonly,
		nativeShells:   p.nativeShells.clone(),
		Frame:          frame,
		seq:            &sequential{},
		owner:          false,
	}
}

// Status reports the last command status, the value `$?` expands to.
func (p *Program) Status() int {
	if p.Session == nil {
		return 0
	}
	return p.Session.Status()
}

// SetStatus records a command status.
func (p *Program) SetStatus(code int) {
	if p.Session != nil {
		p.Session.SetStatus(code)
	}
}

// Fail reports err on the program's standard error and sets the status. It is
// the one diagnostic path: the status is err's own [ExitStatus] when it
// carries one — 2 for a readonly violation or an unrecovered panic, whatever a
// typed projection failure declares — and 1 otherwise.
//
// Fail reports; it does not unwind. A guard failure that must abandon the body
// panics with its typed error instead, which lets the body's defers run and is
// caught by [Program.Run].
func (p *Program) Fail(err error) {
	if err == nil {
		return
	}
	p.SetStatus(failureStatus(err))
	fmt.Fprintln(p.stderr(), err)
}

// ReceiveFailure settles a failed channel receive without abandoning the next
// source statement. Cancellation is deferred until task failure arbitration,
// so a sibling's real error is the only diagnostic. Other channel failures
// report once here with source status 2. The generated caller tracks whether
// the receive binding was established; a channel's normal closed zero value
// with err == nil is still a present binding.
func (p *Program) ReceiveFailure(err error) {
	if err == nil {
		return
	}
	if failureRank(err) != 1 {
		p.Fail(channelOperationError(err))
		return
	}
	if p.seq == nil {
		p.seq = &sequential{}
	}
	p.seq.mu.Lock()
	p.seq.operationErr = rankFailures(p.seq.operationErr, err)
	p.seq.mu.Unlock()
}

func (p *Program) operationFailure() error {
	if p.seq == nil {
		return nil
	}
	p.seq.mu.Lock()
	defer p.seq.mu.Unlock()
	err := p.seq.operationErr
	p.seq.operationErr = nil
	return err
}

// failureStatus is the generic recognition of a runtime failure's status. It
// matches by interface, not by concrete type, so a guard error this package
// does not know about — a projection failure defined elsewhere — is honoured
// without an edit here.
func failureStatus(err error) int {
	var coded interface{ ExitStatus() int }
	if errors.As(err, &coded) {
		if status := coded.ExitStatus(); status > 0 {
			return status
		}
	}
	return 1
}

func (p *Program) stdout() io.Writer {
	if p.Session != nil {
		if w := p.Session.Stdio().Out; w != nil {
			return w
		}
	}
	return os.Stdout
}

func (p *Program) stderr() io.Writer {
	if p.Session != nil {
		if w := p.Session.Stdio().Err; w != nil {
			return w
		}
	}
	return os.Stderr
}

// wrote records the outcome of one output operation. A write that did not
// reach its destination is deliberately not a command failure: the engine's
// echo and printf report status 0 whether or not the write succeeded, and a
// compiled program that invented a failure there would diverge from the script
// it was compiled from. The error is still returned, because the caller — not
// this runtime — decides whether a lost write matters to it.
func (p *Program) wrote(err error) error {
	p.SetStatus(0)
	return err
}

// Echo writes its arguments separated by spaces and followed by a newline, the
// way the shell's echo does for the word forms this foundation supports.
func (p *Program) Echo(args ...any) error {
	words := make([]string, len(args))
	for i, v := range args {
		words[i] = Word(v)
	}
	_, err := fmt.Fprintln(p.stdout(), strings.Join(words, " "))
	return p.wrote(err)
}

// Printf implements the foundation's %s, %d, %% and escape subset, including
// format recycling and omitted arguments. Unsupported conversions fail rather
// than quietly using Go fmt's different semantics.
//
// A format failure is a command failure at status 1, as it is in the engine: a
// malformed format writes nothing, and a conversion that could not read its
// argument still writes its output and then reports. A failed write is neither,
// for the reason [Program.wrote] gives.
func (p *Program) Printf(format string, args ...any) error {
	text, ok, err := printfText(format, args)
	if !ok {
		p.SetStatus(1)
		return err
	}
	_, writeErr := io.WriteString(p.stdout(), text)
	if err != nil {
		p.SetStatus(1)
	} else {
		p.SetStatus(0)
	}
	if writeErr != nil {
		return writeErr
	}
	return err
}

// Print writes its arguments with Go's print spacing rule.
func (p *Program) Print(args ...any) {
	_, _ = fmt.Fprint(p.stdout(), args...)
}

// Println writes its arguments space separated and followed by a newline.
func (p *Program) Println(args ...any) {
	_, _ = fmt.Fprintln(p.stdout(), args...)
}

// printfText renders the printf subset. ok is false for a malformed format, in
// which case nothing may be written; a conversion error that the shell reports
// while still producing output returns ok true and a non-nil err.
func printfText(format string, args []any) (text string, ok bool, err error) {
	var firstError error
	var output strings.Builder
	index := 0
	for {
		start := index
		for i := 0; i < len(format); i++ {
			c := format[i]
			if c == '\\' && i+1 < len(format) {
				i++
				switch format[i] {
				case 'n':
					output.WriteByte('\n')
				case 't':
					output.WriteByte('\t')
				case 'r':
					output.WriteByte('\r')
				case '\\':
					output.WriteByte('\\')
				case 'a':
					output.WriteByte('\a')
				case 'b':
					output.WriteByte('\b')
				case 'f':
					output.WriteByte('\f')
				case 'v':
					output.WriteByte('\v')
				case 'c':
					// \c ends the output right here, including any
					// remaining format recycling.
					return output.String(), true, firstError
				default:
					return "", false, fmt.Errorf("printf: unsupported escape \\%c", format[i])
				}
				continue
			}
			if c != '%' {
				output.WriteByte(c)
				continue
			}
			i++
			if i >= len(format) {
				return "", false, fmt.Errorf("printf: missing format character")
			}
			if format[i] == '%' {
				output.WriteByte('%')
				continue
			}
			if format[i] != 's' && format[i] != 'd' {
				return "", false, fmt.Errorf("printf: unsupported format character %c", format[i])
			}
			value := ""
			if index < len(args) {
				value = Word(args[index])
			}
			index++
			if format[i] == 's' {
				output.WriteString(value)
			} else {
				var n int64
				var convErr error
				if value != "" {
					n, convErr = strconv.ParseInt(value, 0, 64)
					if convErr != nil {
						if firstError == nil {
							firstError = fmt.Errorf("printf: %s: invalid number", value)
						}
						n = 0
					}
				}
				output.WriteString(strconv.FormatInt(n, 10))
			}
		}
		if index >= len(args) || index == start {
			break
		}
	}
	return output.String(), true, firstError
}

// PushPanic records a raised panic and returns the value to hand to Go's
// panic. The recorded chain is what an unrecovered panic is reported from, so
// a panic raised while another one is unwinding names both.
func (p *Program) PushPanic(v any) any {
	message := fmt.Sprint(v)
	if p.seq != nil {
		p.seq.mu.Lock()
		p.seq.panics = append(p.seq.panics, message)
		p.seq.mu.Unlock()
	}
	return message
}

// PopPanic drops the newest recorded panic, which is what recovering one does.
func (p *Program) PopPanic() {
	if p.seq == nil {
		return
	}
	p.seq.mu.Lock()
	if n := len(p.seq.panics); n > 0 {
		p.seq.panics = p.seq.panics[:n-1]
	}
	p.seq.mu.Unlock()
}

// Recovered completes a recover: it takes the value Go's recover produced and
// returns the value the source binding observes, updating the panic chain and
// the status. Recovering nothing yields the empty string and status 1, which
// is how a script tells "no panic was active" from a recovered empty payload.
//
// The direct-only rule is Go's own: recover only works when it is called by a
// deferred function of the panicking frame. This runtime adds no policy of its
// own on top of it.
func (p *Program) Recovered(v any) any {
	if v == nil {
		p.SetStatus(1)
		return ""
	}
	p.PopPanic()
	p.SetStatus(0)
	return v
}

// PanicError is an unrecovered panic, reported the way the source contract
// reports it rather than as a Go stack trace. A panic raised while another was
// unwinding names both, oldest first.
type PanicError struct {
	// Value is the value Go's recover produced.
	Value any

	// Chain is the recorded panic chain, oldest first.
	Chain []string
}

func (e *PanicError) Error() string {
	var out strings.Builder
	for i, message := range e.Chain {
		if i > 0 {
			out.WriteString("\n\t")
		}
		out.WriteString("panic: " + message)
	}
	return out.String()
}

// ExitStatus is the engine's status for an unrecovered panic.
func (*PanicError) ExitStatus() int { return 2 }

// ExitError carries a status that is not attached to any other failure value.
type ExitError struct {
	Status int
	Err    error
}

func (e *ExitError) Error() string {
	if e.Err != nil {
		return e.Err.Error()
	}
	return "exit status " + strconv.Itoa(e.Status)
}

func (e *ExitError) ExitStatus() int { return e.Status }
func (e *ExitError) Unwrap() error   { return e.Err }

// ExitCode is the status a finished program should exit with. It recognises a
// failure's status generically, through the ExitStatus() int interface, so a
// guard error defined outside this package needs no registration.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	return failureStatus(err)
}

// Run runs the program's body and then shuts the program down. It returns the
// failure to report and never prints or exits: the generated entry reports the
// result once with [Program.Fail] and exits with [Program.Status], which is
// what keeps the exit decision in the generated program rather than here.
//
// Run catches every abort the body can raise:
//
//   - a channel abort ([ChannelAbort]) becomes its original error, so
//     cancellation ranking still sees the real cause;
//   - a panicked error carrying ExitStatus() — a readonly violation, a typed
//     projection failure — is returned unchanged, keeping its status;
//   - anything else, including a native Go panic, becomes a [PanicError] with
//     the source panic contract's text and status 2.
//
// The body's own defers unwind normally on every one of those paths, before
// Run sees the failure at all.
//
// Shutdown is ordered: the group is cancelled first, so tasks blocked on owned
// channel operations are released rather than keeping a finished program
// alive; then tasks are joined, then the session is closed (its shell runs its
// EXIT trap and releases the backend), and only then — and only for the
// program that owns the scope — is channel authority revoked. A task entry
// never revokes the shared scope, so a sibling still running does not lose its
// channels underneath it.
//
// The cancellation that shutdown itself causes is not a failure. A task left
// blocked when the body ends is released and reports nothing, exactly as the
// engine's task runtime records nothing for a cancelled task. A cancellation
// the program was handed from outside is a real outcome and is still reported.
//
// A genuine failure outranks a cancellation-class one: when a task's failure
// cancelled an operation the body was blocked on, the reported error is the
// task's, not the cancellation the body observed.
func (p *Program) Run(body func(*Program)) error {
	var bodyErr error
	if p.owner && p.nativeContinuation != nil {
		bodyErr = p.nativeContinuation.acquire(p.Context)
		if bodyErr == nil {
			defer p.nativeContinuation.release()
		}
	}
	if bodyErr == nil {
		bodyErr = rankFailures(p.runBody(body), p.operationFailure())
	}

	if p.Session != nil {
		// The end of the body is the structured lifetime boundary, on the
		// failing path and the successful one alike: cancelling before the
		// join releases tasks blocked on owned operations, so a program is
		// not kept alive forever by work nothing is waiting for. This is the
		// engine's own task shutdown order.
		p.Session.Cancel()
		taskErr := p.Session.Join()
		closeErr := p.Session.Close()

		// A task that only ever observed this shutdown's own cancellation did
		// not fail. The engine's task runtime records nothing for a cancelled
		// task, so a script whose last act is to leave a task blocked ends
		// silently at status 0; a compiled program must too. Close reports the
		// same primary failure Join did, so it is filtered the same way.
		taskErr = p.withoutShutdownCancellation(taskErr)
		closeErr = p.withoutShutdownCancellation(closeErr)
		bodyErr = rankFailures(bodyErr, taskErr)
		if bodyErr == nil {
			// Close reports the same primary task failure Join already
			// returned, so it only adds the backend's own shutdown failure.
			bodyErr = closeErr
		}
	}
	if p.owner && p.Channels != nil {
		p.Channels.Close()
	}
	return bodyErr
}

// runBody runs the body and converts every abort into a failure value.
func (p *Program) runBody(body func(*Program)) (err error) {
	defer func() {
		if v := recover(); v != nil {
			err = p.aborted(v)
		}
	}()
	if body != nil {
		body(p)
	}
	return nil
}

// aborted classifies a recovered value.
func (p *Program) aborted(v any) error {
	if _, ok := v.(DeclarationAbort); ok {
		return nil
	}
	if _, ok := v.(ShellExit); ok {
		return nil
	}
	if abort, ok := v.(ChannelAbort); ok {
		return channelOperationError(abort.Err)
	}
	if abort, ok := v.(ShellAbort); ok {
		return abort.Err
	}
	if err, ok := v.(error); ok {
		var coded interface{ ExitStatus() int }
		if errors.As(err, &coded) {
			// A typed guard unwind — readonly, projection — keeps its own
			// error and status; it is not a panic report.
			return err
		}
	}
	return &PanicError{Value: v, Chain: p.panicChain(v)}
}

// panicChain takes the recorded chain and settles it. A native panic records
// nothing, and a panic raised during another one's unwind records only itself,
// so the recovered value is appended when it is not already the newest entry.
func (p *Program) panicChain(v any) []string {
	var chain []string
	if p.seq != nil {
		p.seq.mu.Lock()
		chain = p.seq.panics
		p.seq.panics = nil
		p.seq.mu.Unlock()
	}
	newest := fmt.Sprint(v)
	if len(chain) == 0 || chain[len(chain)-1] != newest {
		chain = append(chain, newest)
	}
	return chain
}

// withoutShutdownCancellation drops a failure that is only this shutdown's own
// cancellation.
func (p *Program) withoutShutdownCancellation(err error) error {
	if p.shutdownCancellation(err) {
		return nil
	}
	return err
}

// shutdownCancellation reports whether err is nothing but the cancellation
// this shutdown itself caused. A failure that outranks a cancellation is never
// one — a sibling's genuine failure cancels the group too, and Join already
// prefers it — and neither is a cancellation the program was handed from
// outside: a cancelled or expired program context is a real outcome, and
// suppressing it would hide the reason the program stopped.
func (p *Program) shutdownCancellation(err error) bool {
	if err == nil || !errors.Is(err, context.Canceled) {
		return false
	}
	return p.externalCancellation() == nil
}

// externalCancellation reports a cancellation that did not come from this
// shutdown. Run cancels the session's task group, which is derived from these
// contexts and never cancels them, so an error here is someone else's.
func (p *Program) externalCancellation() error {
	if p.Context != nil {
		if err := p.Context.Err(); err != nil {
			return err
		}
	}
	if p.Session != nil {
		if err := p.Session.Context().Err(); err != nil {
			return err
		}
	}
	return nil
}

// rankFailures picks the failure to report. A genuine failure outranks one
// that is only the shutdown's own cancellation, and a tie keeps the body's.
func rankFailures(bodyErr, taskErr error) error {
	if failureRank(taskErr) > failureRank(bodyErr) {
		return taskErr
	}
	if bodyErr != nil {
		return bodyErr
	}
	return taskErr
}

func failureRank(err error) int {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, context.Canceled),
		errors.Is(err, context.DeadlineExceeded),
		errors.Is(err, ErrChannelScopeClosed),
		errors.Is(err, ErrSessionClosed):
		return 1
	default:
		return 2
	}
}
