package interp

// Sprint: #248; Story: #702; Story-ID: f330582c10c8

import (
	"errors"
	"os/exec"
)

// forwardedDeathWriteError classifies a request write that found the control
// connection closed while its own context is live. When the helper has
// already died of a program signal this host forwarded to it, a sibling task
// adopted that death and closed the session; the write belongs to the same
// program termination and answers with it (a forwarded bashPPNativeExit, so
// the task unwinds silently, see bashPPNativeExitStatus) instead of surfacing
// as an independent "task failed: write … use of closed network connection".
// Any other closed write stays the error it is.
func (s *bashPPNativeSession) forwardedDeathWriteError(err error) error {
	if s.done == nil {
		return err
	}
	select {
	case <-s.done:
	default:
		return err
	}
	s.mu.Lock()
	waitErr, forwarded := s.waitErr, s.forwardedSignal > 0
	s.mu.Unlock()
	var exit *exec.ExitError
	if !forwarded || !errors.As(waitErr, &exit) || exit.ExitCode() >= 0 {
		return err
	}
	return s.programExitError(waitErr)
}
