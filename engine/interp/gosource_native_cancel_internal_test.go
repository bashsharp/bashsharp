package interp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os/exec"
	"strings"
	"syscall"
	"testing"
)

func cancellationSession(t *testing.T) *bashPPNativeSession {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			accepted <- conn
		}
	}()
	client, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		listener.Close()
		t.Fatal(err)
	}
	server := <-accepted
	listener.Close()
	finished := make(chan struct{})
	go func() { defer close(finished); var q bashPPBridgeRequest; _ = json.NewDecoder(server).Decode(&q) }()
	t.Cleanup(func() { client.Close(); server.Close(); <-finished })
	return &bashPPNativeSession{conn: client, done: make(chan struct{})}
}

func TestGoSourceNativeCancellationClassification(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, marked := range []bool{false, true} {
		s := cancellationSession(t)
		if marked {
			s.closeCanceled(ctx.Err())
		} else {
			s.close()
		}
		_, writeErr := s.conn.Write([]byte("x"))
		if !errors.Is(writeErr, net.ErrClosed) {
			t.Fatalf("requires actual locally closed TCP socket: %v", writeErr)
		}
		got := s.closedWriteError(ctx, writeErr)
		if marked && !errors.Is(got, context.Canceled) || !marked && got != writeErr {
			t.Fatalf("marked=%v error=%v", marked, got)
		}
		if got := s.closedWriteError(context.Background(), writeErr); got != writeErr {
			t.Fatalf("live parent error suppressed: %v", got)
		}
		for _, realErr := range []error{io.EOF, &net.OpError{Op: "write", Net: "tcp", Err: syscall.ECONNRESET}, errors.New("dependency body failed"), errors.New("use of closed network connection")} {
			if got := s.closedWriteError(ctx, realErr); got != realErr {
				t.Fatalf("genuine error replaced: %v -> %v", realErr, got)
			}
		}
	}
	expired, stop := context.WithCancel(context.Background())
	stop()
	s := cancellationSession(t)
	close(s.done)
	s.closeCanceled(expired.Err())
	_, writeErr := s.conn.Write([]byte("x"))
	if got := s.closedWriteError(expired, writeErr); got != writeErr {
		t.Fatalf("prior process exit relabeled cancellation: %v", got)
	}
}

func TestGoSourceNativeCancellationKeepsProcessFailures(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	exitErr := exec.Command("/bin/sh", "-c", "exit 7").Run()
	signalErr := exec.Command("/bin/sh", "-c", "kill -KILL $$").Run()
	if exitErr == nil || signalErr == nil {
		t.Fatal("missing real process failures")
	}
	s := &bashPPNativeSession{closeCancellation: context.Canceled}
	if got := s.canceledTermination(ctx, exitErr); got != nil {
		t.Fatalf("program exit 7 suppressed: %v", got)
	}
	if got := s.canceledTermination(ctx, errors.New("callback failed")); got != nil {
		t.Fatalf("callback failure suppressed: %v", got)
	}
	if got := s.canceledTermination(context.Background(), nil); got != nil {
		t.Fatalf("live parent exit suppressed: %v", got)
	}
	if got := s.canceledTermination(ctx, nil); !errors.Is(got, context.Canceled) {
		t.Fatalf("canceled helper close: %v", got)
	}
	s.closeCancellation = nil
	if got := s.canceledTermination(ctx, signalErr); got != nil {
		t.Fatalf("unexplained signal suppressed: %v", got)
	}
	s.processCancellation = context.Canceled
	if got := s.canceledTermination(ctx, signalErr); !errors.Is(got, context.Canceled) {
		t.Fatalf("command context kill: %v", got)
	}
	if got := s.canceledTermination(ctx, exitErr); got != nil {
		t.Fatalf("independent exit with late cancellation suppressed: %v", got)
	}
}

func TestGoSourceNativeForwardedSignalBecomesProgramStatus(t *testing.T) {
	err := exec.Command("/bin/sh", "-c", "kill -TERM $$").Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() >= 0 {
		t.Fatalf("requires signaled process failure: %v", err)
	}
	s := &bashPPNativeSession{waitErr: err, forwardedSignal: int(syscall.SIGTERM)}
	var programExit *bashPPNativeExit
	if got := s.programExitError(err); !errors.As(got, &programExit) || programExit.status != 143 || !programExit.forwarded {
		t.Fatalf("forwarded SIGTERM error = %#v", got)
	}
	s.forwardedSignal = 0
	if got := s.programExitError(err); errors.As(got, &programExit) {
		t.Fatalf("unexplained signal became program status: %#v", got)
	}
	r := &Runner{bashPPGoTask: true}
	if !r.bashPPNativeExitStatus(&bashPPNativeExit{status: 143, err: err, forwarded: true}) {
		t.Fatal("forwarded program exit was not adopted")
	}
	if r.exit.code != 143 || !r.bashPPTaskCanceled {
		t.Fatalf("task exit code=%d canceled=%v", r.exit.code, r.bashPPTaskCanceled)
	}
}

func TestGoSourceScalarInterruptionCannotBecomeFatal(t *testing.T) {
	var status exitStatus
	status.fatal(&goSourceError{prefix: "original.go:1:1: ", err: errBashPPScalarInterrupted})
	if status.exiting || status.fatalExit || status.err != nil || status.code != 0 {
		t.Fatalf("interruption became fatal: %+v", status)
	}
}

func TestGoSourceForwardedSignalRestoresWiredInterruption(t *testing.T) {
	s := &bashPPNativeSession{forwardedSignal: int(syscall.SIGTERM)}
	text := "original.go:7:2: " + errBashPPScalarInterrupted.Error()
	s.mu.Lock()
	forwarded := s.forwardedSignal > 0
	s.mu.Unlock()
	if !forwarded || !strings.HasSuffix(text, errBashPPScalarInterrupted.Error()) {
		t.Fatal("wired interruption was not recognized under forwarded signal")
	}
}
