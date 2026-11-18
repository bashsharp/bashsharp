package shellrt

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"
)

// TaskFunc is a concurrent task body. It runs against child, an independent
// snapshot of the launching session's state and shell, and must respect ctx.
type TaskFunc func(ctx context.Context, child *Session) error

// TaskError reports which task failed. Ordinal is the launch ordinal, counted
// from zero within the owning session.
type TaskError struct {
	Ordinal uint64
	Err     error
}

func (e *TaskError) Error() string {
	return fmt.Sprintf("shellrt: task %d: %v", e.Ordinal, e.Err)
}

func (e *TaskError) Unwrap() error { return e.Err }

// TaskPanic is the failure a panicking task body produces. The runtime does
// not re-panic: a panic is converted into a task failure so that group
// cancellation and join still run, which is also what the interpreter's own
// task runtime does with a panicking task.
type TaskPanic struct {
	Ordinal uint64
	Value   any
	Stack   []byte
}

func (p *TaskPanic) Error() string {
	return fmt.Sprintf("shellrt: task %d panicked: %v", p.Ordinal, p.Value)
}

// Task is a handle on one launched task. A task is owned by the session that
// launched it: only that session's Join or Close reaps it.
type Task struct {
	ordinal uint64
	child   *Session
	done    chan struct{}

	mu  sync.Mutex
	err error
}

// Ordinal reports the launch ordinal.
func (t *Task) Ordinal() uint64 { return t.ordinal }

// Session returns the task's child session, or nil if the task never started.
func (t *Task) Session() *Session { return t.child }

// Wait blocks until this task finishes and returns its error, if any. Waiting
// on a single task does not reap its siblings; the owning session still has to
// Join or Close.
func (t *Task) Wait() error {
	<-t.done
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.err
}

func (t *Task) finish(err error) {
	t.mu.Lock()
	t.err = err
	t.mu.Unlock()
	close(t.done)
}

// taskFailure is one recorded failure, ranked for primary-failure selection.
type taskFailure struct {
	ordinal  uint64
	err      error
	canceled bool
}

// taskGroup owns every task launched from one session.
type taskGroup struct {
	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.Mutex
	changed  *sync.Cond
	next     uint64
	active   int
	failures []taskFailure
	joined   bool
}

func newTaskGroup(parent context.Context) *taskGroup {
	ctx, cancel := context.WithCancel(parent)
	g := &taskGroup{ctx: ctx, cancel: cancel}
	g.changed = sync.NewCond(&g.mu)
	return g
}

// ErrSessionClosed is returned for work submitted after the owning session has
// been joined or closed.
var ErrSessionClosed = errors.New("shellrt: session already joined")

// Go launches fn as a task against an independent snapshot of this session's
// state and an independent clone of its shell, and returns once the task has
// armed. That launch handshake is what makes launch ordinals, and therefore
// the primary failure, deterministic rather than schedule-dependent.
//
// A task arms when any of these happens first:
//   - the body returns, or panics;
//   - the body enters a blocking runtime operation the session owns
//     ([Session.Shell], [Session.Join], [Session.Close], [Session.Blocking]);
//   - the body calls [Session.Arm] explicitly;
//   - the group's context is cancelled.
//
// The last two are the escape hatches that keep Go from deadlocking. A body
// that blocks on a primitive the runtime does not own — a bare channel receive
// on a channel the runtime never sees — must arm itself, and the compiler is
// obliged to emit that arm (or wrap the block in [Session.Blocking]) before
// emitting the block. Cancellation releases the launcher regardless, so a body
// that violates the obligation stalls its own join rather than wedging the
// launcher and with it the program's shutdown path.
//
// After the owning session is joined or closed, Go returns a task that has
// already failed with [ErrSessionClosed]; it never starts a goroutine that
// nobody would reap.
func (s *Session) Go(fn TaskFunc) *Task {
	snapshot := s.Snapshot()
	g := s.group

	g.mu.Lock()
	ordinal := g.next
	g.next++
	if g.joined || g.ctx.Err() != nil {
		err := error(ErrSessionClosed)
		if !g.joined {
			err = g.ctx.Err()
		}
		g.mu.Unlock()
		t := &Task{ordinal: ordinal, done: make(chan struct{})}
		t.finish(err)
		return t
	}
	g.active++
	g.mu.Unlock()

	t := &Task{ordinal: ordinal, done: make(chan struct{})}

	// A failed clone is the task's failure, reported through the same group
	// bookkeeping as any other: the launcher is released and the group is
	// cancelled, rather than the launch call returning an error the emitted
	// code would have to handle separately.
	taskCtx := ownedTaskContext(g.ctx)
	child, err := s.newChild(taskCtx, snapshot)
	if err != nil {
		g.finish(t, err)
		return t
	}
	t.child = child

	ready := make(chan struct{})
	child.armReady = ready

	go func() {
		err := runTaskBody(taskCtx, ordinal, child, fn)
		// The child owns its own tasks and its own shell clone; close them
		// here, on every exit path including a panic, so nothing a task
		// launched or opened outlives the body that created it.
		if closeErr := child.Close(); err == nil {
			err = closeErr
		}
		g.finish(t, err)
		child.Arm()
	}()

	select {
	case <-ready:
	case <-g.ctx.Done():
	}
	return t
}

// runTaskBody runs fn and converts a panic into a failure value.
func runTaskBody(ctx context.Context, ordinal uint64, child *Session, fn TaskFunc) (err error) {
	defer func() {
		if p := recover(); p != nil {
			err = &TaskPanic{Ordinal: ordinal, Value: p, Stack: debug.Stack()}
		}
	}()
	return fn(ctx, child)
}

// finish records a task's outcome. A genuine failure cancels the group at that
// point, which is the structured-concurrency contract: siblings are not left
// running behind a failure that the owner will only observe at join time.
func (g *taskGroup) finish(t *Task, err error) {
	g.mu.Lock()
	if err != nil {
		canceled := errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
		g.failures = append(g.failures, taskFailure{ordinal: t.ordinal, err: err, canceled: canceled})
		if !canceled {
			g.cancel()
		}
	}
	g.active--
	if g.active == 0 {
		g.changed.Broadcast()
	}
	g.mu.Unlock()
	t.finish(err)
}

// primaryFailureLocked selects the failure a program reports. A failure that
// is only the group's own cancellation ranks below a genuine one, so the
// reported cause is the task that actually failed rather than whichever
// sibling happened to hold the lowest ordinal while blocked. Within a rank the
// lowest launch ordinal wins, which is deterministic by construction because
// the launch handshake orders the ordinals.
func (g *taskGroup) primaryFailureLocked() error {
	var best *taskFailure
	for i := range g.failures {
		f := &g.failures[i]
		switch {
		case best == nil:
		case !f.canceled && best.canceled:
		case f.canceled == best.canceled && f.ordinal < best.ordinal:
		default:
			continue
		}
		best = f
	}
	if best == nil {
		return nil
	}
	return &TaskError{Ordinal: best.ordinal, Err: best.err}
}

// Arm releases the launcher blocked in [Session.Go]. Calling it more than
// once, or on a root session, is a no-op.
func (s *Session) Arm() {
	if s == nil || s.armReady == nil {
		return
	}
	s.armOnce.Do(func() { close(s.armReady) })
}

// Blocking arms the launch handshake and then runs fn. It is how a task body
// blocks on something this runtime does not own without stalling its launcher.
func (s *Session) Blocking(fn func() error) error {
	s.Arm()
	return fn()
}

// Cancel cancels every task owned by this session without waiting for them.
func (s *Session) Cancel() { s.group.cancel() }

// Join waits for every task owned by this session and returns the primary
// failure, or nil. Join does not cancel: it is the end-of-program wait for
// tasks expected to finish on their own. It is idempotent, and once it returns
// no task launched from this session is still running.
func (s *Session) Join() error {
	g := s.group
	g.mu.Lock()
	for g.active != 0 {
		s.Arm()
		g.changed.Wait()
	}
	g.joined = true
	err := g.primaryFailureLocked()
	g.mu.Unlock()
	return err
}

// Close cancels, joins, and releases the session's shell backend. It is the
// shutdown path a generated program uses on every exit, and is idempotent and
// safe to defer. It returns the primary task failure, or nil.
func (s *Session) Close() error {
	s.mu.Lock()
	closed, shell := s.closed, s.shell
	s.closed = true
	s.mu.Unlock()
	if closed {
		return nil
	}
	s.group.cancel()
	err := s.Join()
	if shell != nil {
		// The shutdown context outlives cancellation on purpose: a cancelled
		// program still has to run its EXIT trap and release the backend.
		s.shellMu.Lock()
		var closeErr error
		if blocking, ok := shell.(BlockingShellRunner); ok {
			closeErr = blocking.CloseWithBlocking(context.WithoutCancel(s.base), s.Arm)
		} else {
			s.Arm()
			closeErr = shell.Close(context.WithoutCancel(s.base))
		}
		s.shellMu.Unlock()
		if err == nil && closeErr != nil {
			err = closeErr
		}
	}
	return err
}

// Active reports how many tasks owned by this session are still running. It
// exists for tests and diagnostics; it is racy by nature and must not gate
// program logic.
func (s *Session) Active() int {
	g := s.group
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.active
}
