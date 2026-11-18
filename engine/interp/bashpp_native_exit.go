// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

// Sprint: #118; Story: #52; Story-ID: d564bada90bb
//
// Original programs terminate themselves through their dependencies:
// os.Exit reports a status and syscall.Exec replaces the running image. Both
// end the process that owns the dependency session. The interpreter must
// report the program's own status rather than a generic bridge failure.

import (
	"context"
	"errors"
	"fmt"
	"runtime"
)

// bashPPNativeExit reports that the dependency process terminated on its own
// rather than failing to answer a request. status is the process status; the
// wrapped error is retained so callers can still inspect the original cause.
type bashPPNativeExit struct {
	status    int
	err       error
	forwarded bool
}

func (e *bashPPNativeExit) Error() string {
	return fmt.Sprintf("gosource: program exited with status %d", e.status)
}

func (e *bashPPNativeExit) Unwrap() error { return e.err }

// bashPPNativeExitStatus adopts a dependency-process termination as the
// program's own exit status. It reports whether err was such a termination.
func (r *Runner) bashPPNativeExitStatus(err error) bool {
	var exit *bashPPNativeExit
	if !errors.As(err, &exit) {
		return false
	}
	status := exit.status
	windowsBreak := runtime.GOOS == "windows" && exit.forwarded && status == 0xC000013A
	if status < 0 || status > 255 && !windowsBreak {
		return false
	}
	var refusal error
	if session := r.bashPPTools.bridge; session != nil {
		session.mu.Lock()
		refusal = session.callbackRefusal
		session.mu.Unlock()
	}
	// An interpreted body the dependency called back into — a test function
	// under testing.tRunner — may already have failed with its own
	// diagnostic; the nonzero exit it unwinds with is that failure, not a
	// second, silent outcome.
	if refusal == nil && r.exit.fatalExit {
		var status ExitStatus
		if r.exit.err != nil && !errors.As(r.exit.err, &status) && !errors.Is(r.exit.err, errBashPPNativeExited) {
			refusal = r.exit.err
		}
	}
	r.closeGoSourceBridge()
	code := uint8(status)
	if windowsBreak {
		code = 130 // shell status while unwinding; Run reproduces the full Windows exit.
	}
	r.exit = exitStatus{code: code, exiting: true}
	if c := r.bashPPConcurrent; c != nil {
		c.mu.Lock()
		if c.mainGoexit && !c.exitCalled {
			c.exitCalled = true
			c.exitStatus = status
			c.cancel()
			c.changed.Broadcast()
		}
		c.mu.Unlock()
	}
	if windowsBreak {
		r.bashPPForwardedDeath = status
	} else if exit.forwarded && status > 128 {
		// The dependency process died by a signal an external sender addressed
		// to this host's PID. The program never installed a handler for it, so
		// Run must reproduce the same signal death on this process once the
		// interpreter has finished unwinding; see bashPPForwardedDeath.
		r.bashPPForwardedDeath = status - 128
	}
	if exit.forwarded && r.bashPPGoTask {
		// Every task shares the program's dependency process. A parent signal
		// terminates that one program; sibling tasks must not report its status
		// again as independent `task failed` diagnostics.
		r.bashPPTaskCanceled = true
	}
	if status != 0 {
		// fatalExit keeps the status across the enclosing command and makes
		// every later exit.fatal on this unwind a no-op, so the program's own
		// status is not replaced by a bridge diagnostic.
		r.exit.fatalExit = true
		if refusal != nil {
			r.exit.err = refusal
		} else {
			r.exit.err = ExitStatus(code)
		}
	}
	return true
}

// bashPPNativeRequest issues one bridge request and converts a self-terminated
// dependency process into the program's exit status.
func (r *Runner) bashPPNativeRequest(ctx context.Context, req bashPPEvalRequest, q bashPPBridgeRequest) ([]bashPPBridgeValue, error) {
	if q.Op == "member" && q.Receiver != nil && req.Bridge != nil {
		// A scalar field of the program's heap oracle reads the heap its
		// objects live on; see bashpp_s374_bytes.go.
		if value, ok := bashPPHostMemStatsMember(req.Bridge.id, *q.Receiver, q.Selector); ok {
			return []bashPPBridgeValue{value}, nil
		}
	}
	if values, handled, err := r.goSourceResidentSyncRequest(ctx, q); handled {
		return values, err
	}
	if values, handled, err := goSourceFastStdlibCall(ctx, req, q); handled {
		return values, err
	}
	if value, handled := r.goSourceLocalTimeMillisecond(req, q); handled {
		return []bashPPBridgeValue{value}, nil
	}
	if value, handled := r.goSourceLocalDurationRound(q); handled {
		return []bashPPBridgeValue{value}, nil
	}
	if handled, err := r.goSourceLocalTimeSleep(ctx, req, q); handled {
		return nil, err
	}
	if values, handled := goSourceLocalNativeWriter(req, q); handled {
		return values, nil
	}
	if value, handled := r.goSourceReflectedFunctionPointer(q); handled {
		return []bashPPBridgeValue{value}, nil
	}
	if values, handled, err := r.goSourceLocalReflectRequest(ctx, req, &q); handled {
		return values, err
	}
	q.PointerReadOnly = nativePointerReadOnlyRequest(req, q)
	if q.Receiver != nil && q.Selector != "Write" {
		req.Bridge.revokeLocalWriter(*q.Receiver)
	}
	if bashPPNativeRequestTrace != nil {
		bashPPNativeRequestTrace(q)
	}
	values, err := req.Bridge.request(ctx, req, q)
	if err == nil {
		r.goSourceRememberValueOf(ctx, req, q, values)
		r.goSourceAdoptOriginalHandles(req, values)
	}
	if r.bashPPGoSource && r.bashPPGoTask && ctx.Err() != nil && errors.Is(err, ctx.Err()) {
		// EOF cancels the task lifetime. Native expression callers need the
		// same silent unwind as channel operations, not a scalar diagnostic.
		r.bashPPTaskCanceled = true
		r.exit.fatal(ctx.Err())
		return nil, errBashPPScalarInterrupted
	}
	var callbackGoexit *bashPPCallbackGoexit
	if errors.As(err, &callbackGoexit) {
		r.bashPPRaiseGoexit()
		return nil, errBashPPScalarInterrupted
	}
	var callbackPanic *bashPPCallbackPanic
	if errors.As(err, &callbackPanic) {
		r.bashPPRaise(callbackPanic.value)
		return nil, errBashPPScalarInterrupted
	}
	var nativeExit *bashPPNativeExit
	if err != nil && errors.As(err, &nativeExit) && r.bashPPNativeExitStatus(err) {
		if r.exit.code == 0 {
			// A successful self-termination has no diagnostic to report; the
			// interpreter simply stops with the program's own zero status.
			return nil, nil
		}
		if nativeExit.forwarded {
			// The signal itself is the complete program outcome. Unwind scalar,
			// assignment and task paths without printing the bridge sentinel.
			return nil, errBashPPScalarInterrupted
		}
		return nil, errBashPPNativeExited
	}
	return values, err
}

var bashPPNativeRequestTrace func(bashPPBridgeRequest)

// errBashPPNativeExited unwinds the interpreter after the program's status has
// already been recorded. It carries no diagnostic of its own.
var errBashPPNativeExited = errors.New("gosource: program exited")

type bashPPCallbackPanic struct{ value string }

func (p *bashPPCallbackPanic) Error() string { return "original callback panic: " + p.value }
