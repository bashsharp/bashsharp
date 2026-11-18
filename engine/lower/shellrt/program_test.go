package shellrt_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/lower/shellrt"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// newProgram builds a program whose output and diagnostics are captured, and
// which has no shell backend: everything these tests assert about status,
// output, panics and ownership is the runtime's own, with no interpreter in
// the picture.
func newProgram(t *testing.T, opts ...shellrt.SessionOption) (*shellrt.Program, *lockedBuffer, *lockedBuffer) {
	t.Helper()
	out, diagnostics := &lockedBuffer{}, &lockedBuffer{}
	base := []shellrt.SessionOption{shellrt.WithStdio(nil, out, diagnostics)}
	p, err := shellrt.NewProgram(append(base, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Session.Close() })
	return p, out, diagnostics
}

// TestProgramsAreIndependent is the whole point of threading the state: two
// programs in one process share no status, no output and no panic
// bookkeeping. It runs them concurrently so that -race would catch any global
// left behind.
func TestProgramsAreIndependent(t *testing.T) {
	t.Parallel()

	const programs = 8
	type result struct {
		out    string
		errOut string
		status int
		err    error
	}
	results := make([]result, programs)
	var wg sync.WaitGroup
	for i := range programs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, diagnostics := &lockedBuffer{}, &lockedBuffer{}
			p, err := shellrt.NewProgram(shellrt.WithStdio(nil, out, diagnostics))
			if err != nil {
				results[i] = result{err: err}
				return
			}
			runErr := p.Run(func(p *shellrt.Program) {
				p.Echo("program", i)
				p.SetStatus(i)
				// Each program's tasks are its own, and so is the channel
				// scope they communicate over.
				channel, makeErr := shellrt.MakeChannel[int](p.Channels, 1)
				if makeErr != nil {
					panic(makeErr)
				}
				p.Session.Go(shellrt.ChannelTask(func(ctx context.Context, child *shellrt.Session) error {
					task := p.Child(ctx, child)
					shellrt.MustChannelOperation(shellrt.Send(task.Context, child, task.Channels, channel, i))
					return nil
				}))
				value, _ := shellrt.MustReceive(shellrt.Receive(p.Context, p.Session, p.Channels, channel))
				if value != i {
					panic(fmt.Sprintf("program %d received %d", i, value))
				}
			})
			results[i] = result{out.String(), diagnostics.String(), p.Status(), runErr}
		}()
	}
	wg.Wait()

	for i, got := range results {
		if got.err != nil {
			t.Fatalf("program %d: %v", i, got.err)
		}
		if want := "program " + strconv.Itoa(i) + "\n"; got.out != want {
			t.Errorf("program %d output %q, want %q", i, got.out, want)
		}
		if got.errOut != "" {
			t.Errorf("program %d diagnostics %q", i, got.errOut)
		}
		if got.status != i {
			t.Errorf("program %d status %d", i, got.status)
		}
	}
}

// TestRunReportsTheTaskFailureThatCancelledTheBody pins the ranking: the body
// only ever observes the cancellation its sibling's failure caused, so
// reporting that cancellation would hide the actual cause.
func TestRunReportsTheTaskFailureThatCancelledTheBody(t *testing.T) {
	t.Parallel()

	p, out, _ := newProgram(t)
	channel, err := shellrt.MakeChannel[int](p.Channels, 0)
	if err != nil {
		t.Fatal(err)
	}
	boom := errors.New("the task failed")
	runErr := p.Run(func(p *shellrt.Program) {
		p.Session.Go(func(ctx context.Context, child *shellrt.Session) error { return boom })
		// Nothing will ever send: only the failure's cancellation can
		// release this receive.
		shellrt.MustReceive(shellrt.Receive(p.Context, p.Session, p.Channels, channel))
		p.Echo("unreachable")
	})
	if !errors.Is(runErr, boom) {
		t.Fatalf("Run reported %v, want the task failure", runErr)
	}
	if got := shellrt.ExitCode(runErr); got != 1 {
		t.Fatalf("exit code %d", got)
	}
	if out.String() != "" {
		t.Fatalf("the body continued past the cancelled receive: %q", out.String())
	}
}

// boundedShell is a stub backend that records its shutdown. It exists to
// assert that Run's shutdown reaches the backend exactly once, after the tasks
// are joined, and with a context that cancellation has not poisoned.
type boundedShell struct {
	mu        sync.Mutex
	closes    int
	closedCtx error
	delay     time.Duration
}

func (b *boundedShell) RunShell(ctx context.Context, st *shellrt.State, io shellrt.Stdio, src string) error {
	return nil
}

func (b *boundedShell) Clone(io shellrt.Stdio) (shellrt.ShellRunner, error) {
	return &boundedShell{delay: b.delay}, nil
}

func (b *boundedShell) Close(ctx context.Context) error {
	time.Sleep(b.delay)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closes++
	b.closedCtx = ctx.Err()
	return nil
}

// TestRunShutdownIsOrderedAndBounded covers the cleanup contract: a failing
// body cancels blocked tasks rather than waiting for them, the backend is then
// closed exactly once with a context cancellation has not poisoned, and the
// channel scope is revoked only afterwards.
func TestRunShutdownIsOrderedAndBounded(t *testing.T) {
	t.Parallel()

	shell := &boundedShell{delay: 20 * time.Millisecond}
	p, _, _ := newProgram(t, shellrt.WithShell(shell))
	channel, err := shellrt.MakeChannel[int](p.Channels, 0)
	if err != nil {
		t.Fatal(err)
	}
	blocked := make(chan struct{})
	boom := errors.New("body failed")

	start := time.Now()
	runErr := p.Run(func(p *shellrt.Program) {
		p.Session.Go(shellrt.ChannelTask(func(ctx context.Context, child *shellrt.Session) error {
			task := p.Child(ctx, child)
			close(blocked)
			// Only the shutdown's cancellation can release this.
			shellrt.MustReceive(shellrt.Receive(task.Context, child, task.Channels, channel))
			return nil
		}))
		<-blocked
		panic(boom)
	})
	elapsed := time.Since(start)

	if elapsed > 30*time.Second {
		t.Fatalf("shutdown took %v", elapsed)
	}
	// The body's own failure outranks the cancellation its shutdown caused.
	var reported *shellrt.PanicError
	if !errors.As(runErr, &reported) {
		t.Fatalf("Run reported %T: %v", runErr, runErr)
	}
	if got, want := reported.Error(), "panic: "+boom.Error(); got != want {
		t.Fatalf("report %q, want %q", got, want)
	}
	shell.mu.Lock()
	closes, closedCtx := shell.closes, shell.closedCtx
	shell.mu.Unlock()
	if closes != 1 {
		t.Fatalf("backend closed %d times", closes)
	}
	if closedCtx != nil {
		t.Fatalf("shutdown context was cancelled: %v", closedCtx)
	}
	// The scope the owning Run revoked is closed for good.
	if _, err := shellrt.MakeChannel[int](p.Channels, 0); !errors.Is(err, shellrt.ErrChannelScopeClosed) {
		t.Fatalf("channel scope was not revoked: %v", err)
	}
}

// TestBlockedTaskAtShutdownIsSilentSuccess is the compiled counterpart of
//
//	func blocked(ch) { ch <- 1; }
//	func main() { ch := make(chan int); go blocked(ch) }
//	main()
//
// which the interpreter ends silently at status 0. The end of the body is the
// structured lifetime boundary, so the blocked send is released; but the
// cancellation that release consists of is this shutdown's own doing and is
// not a failure. Reporting it made the artifact exit 1 with a diagnostic the
// script never produces.
func TestBlockedTaskAtShutdownIsSilentSuccess(t *testing.T) {
	t.Parallel()

	p, out, diagnostics := newProgram(t)
	channel, err := shellrt.MakeChannel[int](p.Channels, 0)
	if err != nil {
		t.Fatal(err)
	}
	blocked := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- p.Run(func(p *shellrt.Program) {
			p.Session.Go(shellrt.ChannelTask(func(ctx context.Context, child *shellrt.Session) error {
				task := p.Child(ctx, child)
				close(blocked)
				// Nothing will ever receive: only shutdown releases this.
				shellrt.MustChannelOperation(shellrt.Send(task.Context, child, task.Channels, channel, 1))
				return nil
			}))
			<-blocked
		})
	}()
	select {
	case runErr := <-done:
		if runErr != nil {
			t.Fatalf("Run reported %v, want a silent success", runErr)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("a blocked task kept the program alive")
	}
	if p.Status() != 0 {
		t.Fatalf("status %d, want 0", p.Status())
	}
	if out.String() != "" || diagnostics.String() != "" {
		t.Fatalf("out=%q err=%q, want both empty", out.String(), diagnostics.String())
	}
}

// TestExternalCancellationIsStillReported is the other side of that
// suppression: a program cancelled from outside stopped for a reason, and
// erasing it would hide why.
func TestExternalCancellationIsStillReported(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	out, diagnostics := &lockedBuffer{}, &lockedBuffer{}
	p, err := shellrt.NewProgram(shellrt.WithStdio(nil, out, diagnostics), shellrt.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	channel, err := shellrt.MakeChannel[int](p.Channels, 0)
	if err != nil {
		t.Fatal(err)
	}
	blocked := make(chan struct{})
	runErr := p.Run(func(p *shellrt.Program) {
		p.Session.Go(shellrt.ChannelTask(func(ctx context.Context, child *shellrt.Session) error {
			task := p.Child(ctx, child)
			close(blocked)
			shellrt.MustChannelOperation(shellrt.Send(task.Context, child, task.Channels, channel, 1))
			return nil
		}))
		<-blocked
		cancel()
	})
	if !errors.Is(runErr, context.Canceled) {
		t.Fatalf("Run reported %v, want the external cancellation", runErr)
	}
}

// TestSiblingFailureIsNotSuppressedAsCancellation: a task failure cancels its
// siblings, so the suppression must not swallow the failure that caused the
// cancellation in the first place.
func TestSiblingFailureIsNotSuppressedAsCancellation(t *testing.T) {
	t.Parallel()

	p, _, _ := newProgram(t)
	channel, err := shellrt.MakeChannel[int](p.Channels, 0)
	if err != nil {
		t.Fatal(err)
	}
	boom := errors.New("the second task failed")
	blocked := make(chan struct{})
	runErr := p.Run(func(p *shellrt.Program) {
		p.Session.Go(shellrt.ChannelTask(func(ctx context.Context, child *shellrt.Session) error {
			task := p.Child(ctx, child)
			close(blocked)
			shellrt.MustChannelOperation(shellrt.Send(task.Context, child, task.Channels, channel, 1))
			return nil
		}))
		<-blocked
		if err := p.Session.Go(func(ctx context.Context, child *shellrt.Session) error {
			return boom
		}).Wait(); err == nil {
			panic("the second task did not fail")
		}
	})
	if !errors.Is(runErr, boom) {
		t.Fatalf("Run reported %v, want the task failure", runErr)
	}
}

// TestTaskEntryDoesNotRevokeSharedChannels is the other half of ownership: a
// task's own Run closes its child session and settles its own panics, but the
// channel scope belongs to the owner and its siblings still need it.
func TestTaskEntryDoesNotRevokeSharedChannels(t *testing.T) {
	t.Parallel()

	p, _, _ := newProgram(t)
	channel, err := shellrt.MakeChannel[string](p.Channels, 1)
	if err != nil {
		t.Fatal(err)
	}
	var taskErr error
	runErr := p.Run(func(p *shellrt.Program) {
		task := p.Session.Go(func(ctx context.Context, child *shellrt.Session) error {
			entry := p.Child(ctx, child)
			return entry.Run(func(entry *shellrt.Program) {
				entry.SetStatus(3)
			})
		})
		taskErr = task.Wait()
		// The scope the task's Run left alone is still usable here.
		shellrt.MustChannelOperation(shellrt.Send(p.Context, p.Session, p.Channels, channel, "still open"))
		value, _ := shellrt.MustReceive(shellrt.Receive(p.Context, p.Session, p.Channels, channel))
		if value != "still open" {
			panic("lost value " + value)
		}
		if p.Status() != 0 {
			panic("the child session's status reached its owner")
		}
	})
	if runErr != nil {
		t.Fatal(runErr)
	}
	if taskErr != nil {
		t.Fatal(taskErr)
	}
}

// TestReadonlyGuardUnwindKeepsItsStatus: a fatal guard failure abandons the
// body, runs its defers on the way out, and is reported with the guard's own
// status rather than a panic's.
func TestReadonlyGuardUnwindKeepsItsStatus(t *testing.T) {
	t.Parallel()

	p, out, diagnostics := newProgram(t)
	value := 1
	if err := p.Readonly.Mark("value", &value); err != nil {
		t.Fatal(err)
	}
	cleanedUp := false
	runErr := p.Run(func(p *shellrt.Program) {
		defer func() { cleanedUp = true }()
		if guard := p.Readonly.CheckAssign(&value); guard != nil {
			panic(guard)
		}
		p.Echo("unreachable")
	})
	if !cleanedUp {
		t.Fatal("the body's defers did not run")
	}
	var readonly *shellrt.ReadonlyError
	if !errors.As(runErr, &readonly) {
		t.Fatalf("Run reported %T: %v", runErr, runErr)
	}
	if got := shellrt.ExitCode(runErr); got != 2 {
		t.Fatalf("exit code %d", got)
	}
	// Run itself neither prints nor sets a status: the entry reports once.
	if out.String() != "" || diagnostics.String() != "" {
		t.Fatalf("Run wrote out=%q err=%q", out.String(), diagnostics.String())
	}
	p.Fail(runErr)
	if p.Status() != 2 {
		t.Fatalf("status %d", p.Status())
	}
	if got, want := diagnostics.String(), readonly.Error()+"\n"; got != want {
		t.Fatalf("diagnostics %q, want %q", got, want)
	}
	if out.String() != "" {
		t.Fatalf("a typed unwind leaked to stdout: %q", out.String())
	}
}

// TestMarkedCallableDenialRunsNoBody covers the entry check: the marker is
// checked before any of the callable's work, the denial is the engine's
// message at status 1, and the body never runs.
func TestMarkedCallableDenialRunsNoBody(t *testing.T) {
	t.Parallel()

	p, out, diagnostics := newProgram(t)
	runErr := p.Run(func(p *shellrt.Program) {
		body, err := p.Enter(shellrt.Site{Name: "assist", File: "unit.bpp", Line: 3}, true)
		if err != nil {
			// The prolog's shape: report once, return the zero results.
			p.Fail(err)
			return
		}
		body.Echo("unreachable")
	})
	if runErr != nil {
		t.Fatal(runErr)
	}
	if p.Status() != 1 {
		t.Fatalf("status %d", p.Status())
	}
	if out.String() != "" {
		t.Fatalf("a denied callable produced output: %q", out.String())
	}
	want := "unit.bpp: line 3: assist: agentic action requires an explicit agentic { ...; } scope\n"
	if got := diagnostics.String(); got != want {
		t.Fatalf("diagnostics %q, want %q", got, want)
	}
}

// TestEnterAndBlockShareSequentialBookkeeping: derived regions are separate
// frames and separate contexts over one shared panic chain.
func TestEnterAndBlockShareSequentialBookkeeping(t *testing.T) {
	t.Parallel()

	p, _, _ := newProgram(t)
	runErr := p.Run(func(p *shellrt.Program) {
		block := p.Block()
		if !block.Frame.Agentic() || p.Frame.Agentic() {
			t.Error("Block mutated its receiver's frame")
		}
		if !shellrt.Agentic(block.Context) || shellrt.Agentic(p.Context) {
			t.Error("the derived context does not carry the derived frame")
		}
		body, err := block.Enter(shellrt.Site{Name: "assist"}, true)
		if err != nil {
			t.Fatal(err)
		}
		if !shellrt.Agentic(body.Context) {
			t.Error("a marked body observes assistance off")
		}
		// The panic raised in one region and the one raised while it unwinds
		// in another region are one chain, because they are one execution.
		defer func() { panic(body.PushPanic("second")) }()
		panic(block.PushPanic("first"))
	})
	if got, want := runErr.Error(), "panic: first\n\tpanic: second"; got != want {
		t.Fatalf("report %q, want %q", got, want)
	}
	if got := shellrt.ExitCode(runErr); got != 2 {
		t.Fatalf("exit code %d", got)
	}
}

// TestRecoveryPopsTheChainAndSetsTheStatus mirrors the engine's direct-only
// recover contract: a recovered panic leaves nothing to report and status 0,
// and recovering nothing yields the empty payload at status 1.
func TestRecoveryPopsTheChainAndSetsTheStatus(t *testing.T) {
	t.Parallel()

	p, out, _ := newProgram(t)
	runErr := p.Run(func(p *shellrt.Program) {
		func() {
			defer func() {
				p.Echo("got", p.Recovered(recover()), "status", p.Status())
			}()
			panic(p.PushPanic("boom"))
		}()
		func() {
			defer func() {
				value := p.Recovered(recover())
				p.Echo("none", "["+shellrt.Word(value)+"]", "status", p.Status())
			}()
		}()
		p.Echo("resumed")
	})
	if runErr != nil {
		t.Fatalf("a recovered panic was reported: %v", runErr)
	}
	want := "got boom status 0\nnone [] status 1\nresumed\n"
	if got := out.String(); got != want {
		t.Fatalf("output %q, want %q", got, want)
	}
}

// TestChildPanicBookkeepingIsForked: a task's panic settles inside the task
// and leaves the owner's own chain untouched, which is what makes a report
// name only the panics of the execution it belongs to.
func TestChildPanicBookkeepingIsForked(t *testing.T) {
	t.Parallel()

	p, _, _ := newProgram(t)
	var taskErr error
	runErr := p.Run(func(p *shellrt.Program) {
		task := p.Session.Go(func(ctx context.Context, child *shellrt.Session) error {
			entry := p.Child(ctx, child)
			return entry.Run(func(entry *shellrt.Program) {
				panic(entry.PushPanic("child"))
			})
		})
		taskErr = task.Wait()
		panic(p.PushPanic("owner"))
	})
	if taskErr == nil || !strings.Contains(taskErr.Error(), "panic: child") {
		t.Fatalf("task failure %v", taskErr)
	}
	if got := shellrt.ExitCode(taskErr); got != 2 {
		t.Fatalf("task exit code %d", got)
	}
	// The owner's report is its own panic alone: the child never pushed onto
	// this chain, and the owner's failure outranks the task's on a tie.
	if got, want := runErr.Error(), "panic: owner"; got != want {
		t.Fatalf("report %q, want %q", got, want)
	}
}

// TestNativePanicUsesTheSourceContract: a Go runtime panic is reported the way
// the source contract reports one, at status 2, not as a Go stack trace.
func TestNativePanicUsesTheSourceContract(t *testing.T) {
	t.Parallel()

	p, _, _ := newProgram(t)
	runErr := p.Run(func(p *shellrt.Program) {
		var values []int
		index := 3
		_ = values[index]
	})
	if got := shellrt.ExitCode(runErr); got != 2 {
		t.Fatalf("exit code %d", got)
	}
	if got := runErr.Error(); !strings.HasPrefix(got, "panic: runtime error: index out of range") {
		t.Fatalf("report %q", got)
	}
	if strings.Contains(runErr.Error(), "goroutine ") {
		t.Fatalf("the report carries a Go stack trace: %q", runErr.Error())
	}
}

// failingWriter fails its first write and succeeds afterwards. Both the
// interpreter and the program runtime are pointed at one, so the two are
// compared on exactly the same I/O failure.
type failingWriter struct {
	mu     sync.Mutex
	failed bool
	buf    bytes.Buffer
}

func (w *failingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.failed {
		w.failed = true
		return 0, errors.New("write failed")
	}
	return w.buf.Write(p)
}

func (w *failingWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

// interpretedStatus runs src through the engine with a writer whose first
// write fails, and returns the status the script ended with and what reached
// standard error. It is the oracle for the two tests below: the compiled
// program's status after the same I/O failure has to be the engine's, not one
// this runtime invented.
func interpretedStatus(t *testing.T, src string) (status int, diagnostics string) {
	t.Helper()
	out := &failingWriter{}
	var stderr strings.Builder
	runner, err := interp.New(
		interp.Lang(syntax.LangBashPP),
		interp.StdIO(nil, out, &stderr),
		interp.Dir(t.TempDir()),
		interp.Env(expand.ListEnviron("PATH=/no-tools")),
	)
	if err != nil {
		t.Fatal(err)
	}
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "oracle.bpp")
	if err != nil {
		t.Fatal(err)
	}
	runErr := runner.Run(context.Background(), file)
	var exit interp.ExitStatus
	switch {
	case runErr == nil:
	case errors.As(runErr, &exit):
		status = int(exit)
	default:
		t.Fatalf("interpreter: %v", runErr)
	}
	return status, stderr.String()
}

// TestOutputWriteFailureIsNotACommandFailure pins the output status against
// the engine rather than against an intuition about I/O. Running `echo a` with
// a writer whose write fails leaves the engine at status 0 with nothing on
// standard error: a lost write is not a failed command there, so a compiled
// program must not report one either. An earlier sticky policy here — a failed
// write that a later successful write could not clear — had no counterpart in
// the source language at all.
func TestOutputWriteFailureIsNotACommandFailure(t *testing.T) {
	t.Parallel()

	status, diagnostics := interpretedStatus(t, "echo a\necho b\n")
	if status != 0 || diagnostics != "" {
		t.Fatalf("engine oracle changed: status=%d stderr=%q", status, diagnostics)
	}

	out := &failingWriter{}
	stderr := &lockedBuffer{}
	p, err := shellrt.NewProgram(shellrt.WithStdio(nil, out, stderr))
	if err != nil {
		t.Fatal(err)
	}
	runErr := p.Run(func(p *shellrt.Program) {
		// The write fails and says so, but the command did not.
		if echoErr := p.Echo("a"); echoErr == nil {
			t.Error("the failing write was reported as a success")
		}
		if p.Status() != status {
			t.Errorf("status %d after a failed write, engine says %d", p.Status(), status)
		}
		if echoErr := p.Echo("b"); echoErr != nil {
			t.Errorf("the second write failed: %v", echoErr)
		}
		if p.Status() != status {
			t.Errorf("status %d after the next write, engine says %d", p.Status(), status)
		}
	})
	if runErr != nil {
		t.Fatal(runErr)
	}
	if p.Status() != status {
		t.Fatalf("final status %d, engine says %d", p.Status(), status)
	}
	if stderr.String() != diagnostics {
		t.Fatalf("diagnostics %q, engine says %q", stderr.String(), diagnostics)
	}
}

// TestPrintfFormatFailureIsACommandFailure is the other half of that
// distinction: the engine does report a bad conversion, at status 1, and still
// writes what it managed to format.
func TestPrintfFormatFailureIsACommandFailure(t *testing.T) {
	t.Parallel()

	status, diagnostics := interpretedStatus(t, "printf '%d\\n' abc\n")
	if status != 1 {
		t.Fatalf("engine oracle changed: status=%d", status)
	}
	if !strings.Contains(diagnostics, "invalid number") {
		t.Fatalf("engine oracle changed: stderr=%q", diagnostics)
	}

	p, _, _ := newProgram(t)
	runErr := p.Run(func(p *shellrt.Program) {
		if err := p.Printf("%d\n", "abc"); err == nil {
			t.Error("an unreadable conversion argument was accepted")
		}
		// The engine reports `printf: abc: invalid number` and leaves $? at
		// its own status for that command.
		if p.Status() != status {
			t.Errorf("status %d after a bad conversion, engine says %d", p.Status(), status)
		}
	})
	if runErr != nil {
		t.Fatal(runErr)
	}
}

// TestPrintfSubsetAndFormatFailures pins the printf behaviour on the program's
// own stream: the supported subset, format recycling, and a malformed format
// writing nothing at all.
func TestPrintfSubsetAndFormatFailures(t *testing.T) {
	t.Parallel()

	p, out, _ := newProgram(t)
	if err := p.Printf("%s=%d\n", "n", 7); err != nil {
		t.Fatal(err)
	}
	if err := p.Printf("[%s]", "a", "b"); err != nil {
		t.Fatal(err)
	}
	if err := p.Printf("\tx\\c dropped"); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "n=7\n[a][b]\tx"; got != want {
		t.Fatalf("output %q, want %q", got, want)
	}
	if err := p.Printf("%q\n", "x"); err == nil {
		t.Fatal("an unsupported conversion was accepted")
	}
	if got, want := out.String(), "n=7\n[a][b]\tx"; got != want {
		t.Fatalf("a malformed format wrote %q", got)
	}
	if p.Status() != 1 {
		t.Fatalf("status %d after a format failure", p.Status())
	}
}

// TestChildRetainsChannelAndReadonlyIdentity: a task shares its owner's
// channel authority and readonly marks by identity, not by copy.
func TestChildRetainsChannelAndReadonlyIdentity(t *testing.T) {
	t.Parallel()

	p, _, _ := newProgram(t)
	value := 1
	if err := p.Readonly.Mark("value", &value); err != nil {
		t.Fatal(err)
	}
	runErr := p.Run(func(p *shellrt.Program) {
		task := p.Session.Go(func(ctx context.Context, child *shellrt.Session) error {
			entry := p.Child(ctx, child)
			if entry.Channels != p.Channels {
				return errors.New("the task lost its owner's channel scope")
			}
			if entry.Readonly != p.Readonly {
				return errors.New("the task lost its owner's readonly marks")
			}
			if entry.Session != child {
				return errors.New("the task did not take its child session")
			}
			if entry.Readonly.CheckAssign(&value) == nil {
				return errors.New("a readonly mark did not reach the task")
			}
			return nil
		})
		if err := task.Wait(); err != nil {
			panic(err)
		}
	})
	if runErr != nil {
		t.Fatal(runErr)
	}
}

// programArtifact builds one real binary from source and returns a runner for
// it. The generated entry shape is the one the compiler emits: Run, then one
// Fail, then os.Exit — every exit decision in the program, none in shellrt.
func programArtifact(t *testing.T, source string) func(mode string) (string, string, int) {
	t.Helper()
	if strings.Contains(source, "interp") || strings.Contains(source, "shellexec") {
		t.Fatal("a typed-only artifact must link no interpreter")
	}
	_, this, _, _ := runtime.Caller(0)
	root := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(this))))
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	module := "module programfixture\n\ngo " + strings.TrimPrefix(runtime.Version(), "go") +
		"\n\nrequire github.com/bashsharp/bashsharp v0.0.0\n\nreplace github.com/bashsharp/bashsharp => " + root + "\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(module), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)
	binary := filepath.Join(dir, "program")
	build := exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-o", binary, ".")
	build.Dir = dir
	build.Env = append(os.Environ(), "GOWORK=off", "GOTOOLCHAIN=local", "GOFLAGS=-mod=mod", "GOPROXY=off")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	// The source is gone and PATH has no tools: whatever the program does, it
	// does from what it linked.
	if err := os.Remove(filepath.Join(dir, "main.go")); err != nil {
		t.Fatal(err)
	}
	return func(mode string) (string, string, int) {
		t.Helper()
		cmd := exec.CommandContext(ctx, binary, mode)
		cmd.Dir = t.TempDir()
		cmd.Env = []string{"PATH=/no-tools"}
		var out, diagnostics bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &diagnostics
		status := 0
		if err := cmd.Run(); err != nil {
			var exit *exec.ExitError
			if !errors.As(err, &exit) {
				t.Fatalf("%v\n%s", err, diagnostics.String())
			}
			status = exit.ExitCode()
		}
		return out.String(), diagnostics.String(), status
	}
}

const programEntrySource = `package main

import (
	"context"
	"errors"
	"os"

	"github.com/bashsharp/bashsharp/engine/lower/shellrt"
)

// blocked is the shape the compiler emits for a private callable: the threaded
// program, and a channel operation that unwinds through MustChannelOperation.
func blocked(p *shellrt.Program, ch chan int) {
	shellrt.MustChannelOperation(shellrt.Send(p.Context, p.Session, p.Channels, ch, 1))
}

func main() {
	p, err := shellrt.NewProgram()
	if err != nil {
		os.Stderr.WriteString(err.Error() + "\n")
		os.Exit(1)
	}
	mode := ""
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}
	if runErr := p.Run(func(p *shellrt.Program) {
		switch mode {
		case "ok":
			p.Echo("hello")
		case "status":
			p.Echo("before")
			p.SetStatus(7)
		case "panic":
			p.Echo("before")
			panic(p.PushPanic("boom"))
		case "nested":
			defer func() { panic(p.PushPanic("second")) }()
			panic(p.PushPanic("first"))
		case "recovered":
			func() {
				defer func() { p.Echo("caught", p.Recovered(recover())) }()
				panic(p.PushPanic("caught me"))
			}()
			p.Echo("resumed")
		case "native":
			var values []int
			index := 3
			_ = values[index]
		case "readonly":
			value := 1
			if markErr := p.Readonly.Mark("value", &value); markErr != nil {
				panic(markErr)
			}
			if guard := p.Readonly.CheckAssign(&value); guard != nil {
				panic(guard)
			}
			p.Echo("unreachable")
		case "denied":
			body, enterErr := p.Enter(shellrt.Site{Name: "assist", File: "unit.bpp", Line: 3}, true)
			if enterErr != nil {
				p.Fail(enterErr)
				return
			}
			body.Echo("unreachable")
		case "task":
			p.Session.Go(func(ctx context.Context, child *shellrt.Session) error {
				return errors.New("the task failed")
			})
			p.Echo("launched")
		case "blocked":
			// func blocked(ch) { ch <- 1; }
			// func main() { ch := make(chan int); go blocked(ch) }
			ch := shellrt.MustChannel(shellrt.MakeChannel[int](p.Channels, 0))
			p.Session.Go(shellrt.ChannelTask(func(taskCtx shellrt.TaskContext, taskSession *shellrt.Session) error {
				blocked(p.Child(taskCtx, taskSession), ch)
				return nil
			}))
		}
	}); runErr != nil {
		p.Fail(runErr)
	}
	os.Exit(p.Status())
}
`

// TestProgramEntryArtifact runs the real thing: a built binary whose only exit
// decision is its own, checked for the status and the diagnostic each failure
// shape owes the source contract.
func TestProgramEntryArtifact(t *testing.T) {
	t.Parallel()

	run := programArtifact(t, programEntrySource)
	tests := []struct {
		mode, out, diagnostics string
		status                 int
		prefix                 bool
	}{
		{mode: "ok", out: "hello\n"},
		{mode: "status", out: "before\n", status: 7},
		{mode: "recovered", out: "caught caught me\nresumed\n"},
		{mode: "panic", out: "before\n", diagnostics: "panic: boom\n", status: 2},
		{mode: "nested", diagnostics: "panic: first\n\tpanic: second\n", status: 2},
		{mode: "native", diagnostics: "panic: runtime error: index out of range", status: 2, prefix: true},
		{
			mode:        "readonly",
			diagnostics: "BASHPP-EREADONLY-MUTATION: cannot assign to readonly value \"value\"\n",
			status:      2,
		},
		{
			mode:        "denied",
			diagnostics: "unit.bpp: line 3: assist: agentic action requires an explicit agentic { ...; } scope\n",
			status:      1,
		},
		{mode: "task", out: "launched\n", diagnostics: "shellrt: task 0: the task failed\n", status: 1},
		// The interpreter ends this script silently at status 0; so does the
		// artifact. The task is released by the shutdown it did not cause.
		{mode: "blocked"},
	}
	for _, tc := range tests {
		t.Run(tc.mode, func(t *testing.T) {
			out, diagnostics, status := run(tc.mode)
			if out != tc.out {
				t.Errorf("stdout %q, want %q", out, tc.out)
			}
			switch {
			case tc.prefix:
				if !strings.HasPrefix(diagnostics, tc.diagnostics) {
					t.Errorf("stderr %q, want prefix %q", diagnostics, tc.diagnostics)
				}
			case diagnostics != tc.diagnostics:
				t.Errorf("stderr %q, want %q", diagnostics, tc.diagnostics)
			}
			if strings.Contains(diagnostics, "goroutine ") {
				t.Errorf("the artifact printed a Go stack trace: %q", diagnostics)
			}
			if status != tc.status {
				t.Errorf("exit status %d, want %d", status, tc.status)
			}
		})
	}
}
