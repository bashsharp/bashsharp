package interp

import (
	"context"
	"sync"
	"time"
)

// A default signal sent by an asynchronous shell to $$ belongs to the shell
// owner, even before an exec replacement exists. Its cancellation only stops
// foreground evaluation; the sender retains the caller's cancellation lifetime.
type asyncOwnerSignal struct {
	signal killSig
}

func (s *asyncOwnerSignal) status() SignaledStatus {
	name, _ := signalName(s.signal)
	return SignaledStatus{Status: ExitStatus(128 + sigNum(s.signal)), Signal: sigNum(s.signal), SignalName: "SIG" + name}
}

type asyncSignalRun struct {
	caller         context.Context
	foregroundDone <-chan struct{}
	cancel         context.CancelFunc
}

type asyncSignalContextKey struct{}

type asyncCallerContext struct {
	context.Context
	caller context.Context
	done   chan struct{}
	err    error // written before done closes, then immutable
}

func (c *asyncCallerContext) Err() error {
	select {
	case <-c.done:
		return c.err
	default:
		return nil
	}
}

// A distinct Done channel keeps context's parent-cancel fast path from
// bypassing this bridge's Err when a descendant is created.
func (c *asyncCallerContext) Done() <-chan struct{} { return c.done }

func (c *asyncCallerContext) Deadline() (time.Time, bool) { return c.caller.Deadline() }

func (r *Runner) backgroundContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if !r.asyncList && !r.inBashPPTask() {
		if run, ok := ctx.Value(asyncSignalContextKey{}).(*asyncSignalRun); ok && ctx.Done() == run.foregroundDone {
			// Remove only the known foreground boundary. Preserve values,
			// the caller's deadline and its cancellation cause explicitly.
			child, cancelChild := context.WithCancelCause(context.WithoutCancel(ctx))
			bridge := &asyncCallerContext{Context: child, caller: run.caller, done: make(chan struct{})}
			var first sync.Once
			cancel := func(err, cause error) {
				first.Do(func() {
					bridge.err = err
					cancelChild(cause)
					close(bridge.done)
				})
			}
			stop := context.AfterFunc(run.caller, func() { cancel(run.caller.Err(), context.Cause(run.caller)) })
			if run.caller.Err() != nil {
				cancel(run.caller.Err(), context.Cause(run.caller))
			}
			return bridge, func() {
				stop()
				cancel(context.Canceled, context.Canceled)
			}
		}
	}
	// A distinct Done channel belongs to a caller-created scope. Do not
	// discard its independent cancellation or deadline.
	return context.WithCancel(ctx)
}

func (r *Runner) ownerCallerContext(ctx context.Context) context.Context {
	if run, ok := ctx.Value(asyncSignalContextKey{}).(*asyncSignalRun); ok && ctx.Done() == run.foregroundDone {
		return run.caller
	}
	return ctx
}

func (r *Runner) beginAsyncSignalRun(ctx context.Context) (context.Context, func()) {
	s := r.execReplacement
	if s == nil || s.owner != r {
		return ctx, func() {}
	}
	s.mu.Lock()
	if s.run != nil { // A nested Run still belongs to the active owner call.
		s.mu.Unlock()
		return ctx, func() {}
	}
	foreground, cancel := context.WithCancel(ctx)
	run := &asyncSignalRun{caller: ctx, foregroundDone: foreground.Done(), cancel: cancel}
	s.run = run
	if s.pending != nil {
		cancel()
	}
	s.mu.Unlock()
	return context.WithValue(foreground, asyncSignalContextKey{}, run), func() {
		s.mu.Lock()
		if s.run == run {
			s.run = nil
		}
		s.mu.Unlock()
		cancel()
	}
}

// routeAsyncOwnerSignal serializes an early default signal with publication of
// an exec replacement. A published attempt owns its ready/PID handoff; otherwise
// the foreground owner is interrupted immediately, without waiting for an exec
// or for any background job to complete.
func (r *Runner) routeAsyncOwnerSignal(sig killSig) (*execReplacementAttempt, bool) {
	s := r.execReplacement
	if !r.asyncList || s == nil || s.owner == nil {
		return nil, false
	}
	s.mu.Lock()
	if replacement := s.current.Load(); replacement != nil {
		s.mu.Unlock()
		return replacement, false
	}
	owner := s.owner
	name, _ := signalName(sig)
	owner.sigMu.Lock()
	callback, trapped := owner.trapCallbacks[name]
	ignored := owner.startupIgnored[name] || (trapped && callback == "")
	owner.sigMu.Unlock()
	if name == "KILL" || name == "STOP" {
		// Bash retains their trap metadata for listing, but these signals
		// cannot be caught or ignored.
		trapped, ignored = false, false
	}
	if sigIsZero(sig) || ignored || (!trapped && signalDefaultDoesNotTerminate(sig)) {
		s.mu.Unlock()
		return nil, true
	}
	if trapped {
		s.mu.Unlock()
		owner.queuePendingSignal(name, callback)
		return nil, true
	}
	if s.pending == nil {
		s.pending = &asyncOwnerSignal{signal: sig}
	}
	run := s.run
	s.mu.Unlock()
	if run != nil {
		run.cancel()
	}
	return nil, true
}

func (r *Runner) finishAsyncOwnerSignal() *asyncOwnerSignal {
	s := r.execReplacement
	if s == nil || s.owner != r {
		return nil
	}
	s.mu.Lock()
	pending := s.pending
	s.pending = nil
	s.mu.Unlock()
	if pending == nil {
		return nil
	}
	status := pending.status()
	r.exit = exitStatus{code: uint8(status.Status), err: status, exiting: true}
	return pending
}
