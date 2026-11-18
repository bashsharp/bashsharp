//go:build unix

package polyglot

import (
	"context"
	"errors"
	"syscall"
	"testing"
	"time"
)

// xonsh test_interrupted_process_returncode: a SIGINT delivered to the
// running process is the signal status (bash: 130). A signal with a
// terminating default action (SIGTERM) kills the worker; the command reports
// the death as [WorkerExit] and the next command restarts the worker with
// fresh state — an idle SIGINT, however, is dropped and keeps the state.
func TestPythonCommandSignals(t *testing.T) {
	m := commandModule(t)
	runCommand(t, m, "bump")
	if got := runCommand(t, m, "sigint"); got.Status != 130 || got.Stderr != "" {
		t.Fatalf("sigint = %#v", got)
	}
	if got := runCommand(t, m, "bump"); got.Stdout != "2\n" {
		t.Fatalf("state after sigint: bump = %#v", got)
	}
	// Idle worker: a terminal Ctrl-C between commands must not kill it.
	m.procMu.Lock()
	pid := m.cmd.Process.Pid
	m.procMu.Unlock()
	if err := syscall.Kill(pid, syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if got := runCommand(t, m, "bump"); got.Stdout != "3\n" {
		t.Fatalf("state after idle SIGINT: bump = %#v", got)
	}
	_, err := m.Command(context.Background(), "sigterm", nil, nil)
	var exit *WorkerExit
	if !errors.As(err, &exit) || exit.Signal != int(syscall.SIGTERM) {
		t.Fatalf("sigterm error = %v", err)
	}
	if got := runCommand(t, m, "bump"); got.Stdout != "1\n" {
		t.Fatalf("restart after sigterm: bump = %#v", got)
	}
}
