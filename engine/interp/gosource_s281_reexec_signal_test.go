//go:build full && (darwin || linux)

package interp_test

// Sprint: #281; Story: #811; Story-ID: aa5c046bb543

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"
)

// s281ReexecForwardSource copies its generated launcher to a caller-named path
// so a native test can drive the launcher directly and observe how it treats
// its replayed child when the launcher process is signalled.
const s281ReexecForwardSource = `package main
import (
	"os"
)
func main() {
	launcher, err := os.Executable()
	if err != nil { panic(err) }
	data, err := os.ReadFile(launcher)
	if err != nil { panic(err) }
	renamed := os.Getenv("BASHPP_S281_RENAMED_TOOL")
	if err := os.WriteFile(renamed, data, 0755); err != nil { panic(err) }
}
`

// TestGoSourceS281SelfReexecForwardsInterrupt drives the generated launcher
// directly and measures its lifecycle toward its direct replayed child. The
// launcher installs a signal handler and waits for the child, so a SIGINT or
// SIGTERM delivered to the launcher process alone (not its process group) is
// forwarded to the child, which exits rather than continuing to run after the
// launcher returns. The -V=full identity path has no child to signal and is
// covered separately by the reexec version-probe tests.
//
// This asserts only the launcher's direct-child behavior; it establishes
// nothing about any further interpreter-helper grandchildren. The baseline
// launcher (no forwarding) exits on the signal and leaves the child running,
// which the interrupted-marker wait detects as a bounded failure.
func TestGoSourceS281SelfReexecForwardsInterrupt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the focused child is a POSIX shell that traps INT/TERM")
	}
	for _, tc := range []struct {
		name   string
		args   []string
		signal syscall.Signal
	}{
		{name: "run/SIGINT", signal: syscall.SIGINT},
		{name: "run/SIGTERM", signal: syscall.SIGTERM},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s281AssertLauncherForwards(t, tc.args, tc.signal)
		})
	}
}

func s281AssertLauncherForwards(t *testing.T, args []string, sig syscall.Signal) {
	t.Helper()
	dir := t.TempDir()
	renamed := filepath.Join(dir, "reexec-launcher")
	t.Setenv("BASHPP_S281_RENAMED_TOOL", renamed)

	started := filepath.Join(dir, "child-started")
	interrupted := filepath.Join(dir, "child-interrupted")
	// The child installs its trap, records that it started, then loops sleeping.
	// On INT/TERM it records the signal and exits 0. Without forwarding the child
	// never receives the signal and the interrupted marker never appears.
	script := `trap 'printf x > "$MARK_INT"; exit 0' INT TERM
printf x > "$MARK_STARTED"
n=0
while [ "$n" -lt 300 ]; do sleep 0.1; n=$((n+1)); done`
	plan := []string{"/bin/sh", "-c", script}

	if got := runS281ReexecSourceProgram(t, s281ReexecForwardSource, nil, nil, plan); got != "" {
		t.Fatalf("launcher build output = %q, want empty", got)
	}

	cmd := exec.Command(renamed, args...)
	cmd.Env = append(s281WithoutEnv(os.Environ(), "GOSH_PROG"),
		"MARK_STARTED="+started, "MARK_INT="+interrupted)
	// Put the launcher in its own process group so cleanup can terminate the
	// entire fixture — the launcher, the replayed shell child and its sleeps —
	// even in the baseline where nothing is forwarded and the child would
	// otherwise be orphaned and outlive the test.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start launcher: %v", err)
	}
	pgid := cmd.Process.Pid
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	normalWait := false
	// Cleanup kills the whole fixture process group and, unless the normal wait
	// below already consumed the launcher's exit, reaps the launcher with a
	// bounded wait on the same waited channel. This also runs on the
	// readiness/signal/timeout failure paths, where the launcher was started but
	// its exit was never consumed; without reaping, the cmd.Wait goroutine would
	// leak. It must not wait a second time once normalWait has consumed waited,
	// which would block forever.
	t.Cleanup(func() {
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
		if normalWait {
			return
		}
		select {
		case <-waited:
		case <-time.After(2 * time.Second):
		}
	})

	// Signal only once the child is running: by then wait() has installed the
	// launcher's handler and the child has installed its trap. Signalling earlier
	// would hit the launcher's default signal disposition instead of forwarding.
	s281WaitForFile(t, started, 30*time.Second)
	if err := cmd.Process.Signal(sig); err != nil {
		t.Fatalf("signal launcher: %v", err)
	}
	// The launcher must exit within a bounded window after being signalled.
	select {
	case <-waited:
		normalWait = true
	case <-time.After(30 * time.Second):
		t.Fatalf("launcher did not exit within 30s after %v", sig)
	}
	// Distinguishing assertion: with forwarding the child observes the signal and
	// writes the marker; the baseline launcher exits without forwarding and this
	// bounded wait fails.
	s281WaitForFile(t, interrupted, 10*time.Second)
}

// s281WaitForFile blocks until path exists or the bounded timeout elapses,
// failing the test on timeout so a missing marker is a distinguishing failure
// rather than a silent pass.
func s281WaitForFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, path)
}
