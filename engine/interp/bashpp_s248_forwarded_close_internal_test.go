package interp

// Sprint: #248; Story: #702; Story-ID: f330582c10c8

import (
	"context"
	"errors"
	"net"
	"os/exec"
	"syscall"
	"testing"
)

// TestS248ForwardedDeathClosedWrite: a task whose request write finds the
// control connection closed after the helper died of a forwarded program
// signal reports that program death, not a write failure; a closed write with
// no such death, or after an ordinary exit, stays the error it is.
func TestS248ForwardedDeathClosedWrite(t *testing.T) {
	signaled := exec.Command("/bin/sh", "-c", "kill -KILL $$").Run()
	exited := exec.Command("/bin/sh", "-c", "exit 3").Run()
	var exit *exec.ExitError
	if !errors.As(signaled, &exit) || exit.ExitCode() >= 0 || exited == nil {
		t.Fatalf("requires real process outcomes: %v %v", signaled, exited)
	}
	closedWrite := func(s *bashPPNativeSession) error {
		s.close()
		_, err := s.conn.Write([]byte("x"))
		if !errors.Is(err, net.ErrClosed) {
			t.Fatalf("requires a locally closed socket: %v", err)
		}
		return err
	}
	live := context.Background()

	s := cancellationSession(t)
	close(s.done)
	s.waitErr, s.forwardedSignal = signaled, int(syscall.SIGTERM)
	writeErr := closedWrite(s)
	var programExit *bashPPNativeExit
	if got := s.closedWriteError(live, writeErr); !errors.As(got, &programExit) || !programExit.forwarded || programExit.status != 128+int(syscall.SIGTERM) {
		t.Fatalf("forwarded death: closed write = %#v", got)
	}

	for name, setup := range map[string]func(*bashPPNativeSession){
		"helper alive":       func(s *bashPPNativeSession) { s.forwardedSignal = int(syscall.SIGTERM) },
		"unforwarded signal": func(s *bashPPNativeSession) { close(s.done); s.waitErr = signaled },
		"ordinary exit": func(s *bashPPNativeSession) {
			close(s.done)
			s.waitErr, s.forwardedSignal = exited, int(syscall.SIGTERM)
		},
		"clean helper exit": func(s *bashPPNativeSession) { close(s.done); s.forwardedSignal = int(syscall.SIGTERM) },
	} {
		s := cancellationSession(t)
		setup(s)
		writeErr := closedWrite(s)
		if got := s.closedWriteError(live, writeErr); got != writeErr {
			t.Fatalf("%s: closed write relabeled: %#v", name, got)
		}
	}
}
