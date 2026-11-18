// Copyright (c) 2026, the outpost authors
// See LICENSE for licensing information

//go:build windows

package interp

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Sprint 245, story S245.8: the ntdll suspend/resume primitive and the `fg`
// path built on it, exercised against a real child process. Which signal
// picks which action, and the job-table bookkeeping around it, are proven on
// any host in suspend_routing_test.go and jobsignal_state_test.go.

// suspendHelperEnv makes a re-executed copy of this test binary play the
// child: a process that keeps making observable progress until it is
// stopped. Its value is how many ticks to emit before exiting; 0 means
// forever.
const suspendHelperEnv = "GOSH_SUSPEND_TICKER"

const tickInterval = 5 * time.Millisecond

func init() {
	spec := os.Getenv(suspendHelperEnv)
	if spec == "" {
		return
	}
	limit, _ := strconv.Atoi(spec)
	// os.Stdout is unbuffered, so every tick is immediately visible to the
	// parent — which is what lets it tell "suspended" from "slow".
	for n := 0; limit <= 0 || n < limit; n++ {
		if _, err := fmt.Fprintln(os.Stdout, "tick"); err != nil {
			os.Exit(0)
		}
		time.Sleep(tickInterval)
	}
	os.Exit(0)
}

// ticker is a running child process emitting output at a steady rate, with a
// count of the bytes it has produced so far.
type ticker struct {
	cmd      *exec.Cmd
	seen     atomic.Int64
	waitOnce sync.Once
	waitErr  error
}

func (tk *ticker) pid() int { return tk.cmd.Process.Pid }

// wait reaps the child. It is safe to call from both the test body and the
// cleanup: exec.Cmd.Wait may only run once.
func (tk *ticker) wait() error {
	tk.waitOnce.Do(func() { tk.waitErr = tk.cmd.Wait() })
	return tk.waitErr
}

// startTicker re-executes this test binary as the ticking child. limit is the
// number of ticks before it exits on its own; 0 means it runs until killed.
// The parent drains the pipe continuously: a child that filled a pipe nobody
// reads would block on write and look suspended without being suspended.
func startTicker(t *testing.T, limit int) *ticker {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	tk := &ticker{cmd: exec.Command(exe)}
	tk.cmd.Env = append(os.Environ(), suspendHelperEnv+"="+strconv.Itoa(limit))
	out, err := tk.cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("StdoutPipe: %v", err)
	}
	if err := tk.cmd.Start(); err != nil {
		t.Fatalf("starting the ticker: %v", err)
	}
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		buf := make([]byte, 512)
		for {
			n, err := out.Read(buf)
			tk.seen.Add(int64(n))
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		// Never leave a stopped process behind: a suspended child would
		// survive the test binary.
		_ = resumeProcess(tk.pid())
		_ = tk.cmd.Process.Kill()
		_ = tk.wait()
		<-drained
	})
	return tk
}

// waitForProgress fails unless the child produces more output than mark
// within a generous window.
func waitForProgress(t *testing.T, tk *ticker, mark int64, what string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if tk.seen.Load() > mark {
			return
		}
		time.Sleep(tickInterval)
	}
	t.Fatalf("%s: the child made no progress past %d bytes", what, mark)
}

// holdStill fails unless the child produces nothing at all over a window
// several hundred ticks long, and returns the byte count it froze at.
func holdStill(t *testing.T, tk *ticker, what string) int64 {
	t.Helper()
	// Let anything already in flight land first.
	time.Sleep(40 * tickInterval)
	stopped := tk.seen.Load()
	time.Sleep(100 * tickInterval)
	if grew := tk.seen.Load(); grew != stopped {
		t.Fatalf("%s: the process kept running, %d bytes became %d", what, stopped, grew)
	}
	return stopped
}

func TestSuspendResumeProcess(t *testing.T) {
	tk := startTicker(t, 0)
	waitForProgress(t, tk, 0, "before the suspend")

	if err := suspendProcess(tk.pid()); err != nil {
		t.Fatalf("suspendProcess(%d): %v", tk.pid(), err)
	}
	stopped := holdStill(t, tk, "after suspendProcess")
	// A stopped process is still there, so `kill -0` succeeds on it.
	if err := probeProcess(tk.pid()); err != nil {
		t.Fatalf("probing a suspended process: %v", err)
	}

	if err := resumeProcess(tk.pid()); err != nil {
		t.Fatalf("resumeProcess(%d): %v", tk.pid(), err)
	}
	waitForProgress(t, tk, stopped, "after the resume")
}

// `kill -STOP` and `kill -CONT` must reach the same primitive through the
// signal table, without being diverted to the bashy signal pipe.
func TestSendSignalStopAndContinue(t *testing.T) {
	tk := startTicker(t, 0)
	waitForProgress(t, tk, 0, "before kill -STOP")
	if err := sendSignal(tk.pid(), mustSignal(t, "STOP")); err != nil {
		t.Fatalf("kill -STOP: %v", err)
	}
	stopped := holdStill(t, tk, "after kill -STOP")
	if err := sendSignal(tk.pid(), mustSignal(t, "CONT")); err != nil {
		t.Fatalf("kill -CONT: %v", err)
	}
	waitForProgress(t, tk, stopped, "after kill -CONT")
}

// Resuming a process that was never suspended is SIGCONT to a running
// process: a successful no-op that leaves it running.
func TestResumeProcessNotSuspended(t *testing.T) {
	tk := startTicker(t, 0)
	waitForProgress(t, tk, 0, "before the resume")
	if err := resumeProcess(tk.pid()); err != nil {
		t.Fatalf("resumeProcess on a running process: %v", err)
	}
	waitForProgress(t, tk, tk.seen.Load(), "after the no-op resume")
}

// A stopped process must still be killable, and must report the signal: on
// Unix SIGKILL is delivered to a stopped process without resuming it, and
// `kill -9` on a stopped job is how jobs.tests reaps its sleeps.
func TestKillSuspendedProcess(t *testing.T) {
	tk := startTicker(t, 0)
	waitForProgress(t, tk, 0, "before the suspend")
	if err := suspendProcess(tk.pid()); err != nil {
		t.Fatalf("suspendProcess(%d): %v", tk.pid(), err)
	}
	sig := mustSignal(t, "KILL")
	if err := sendSignal(tk.pid(), sig); err != nil {
		t.Fatalf("kill -KILL on a suspended process: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- tk.wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a killed process exited successfully")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("a suspended process did not die of kill -KILL")
	}
	code := uint32(tk.cmd.ProcessState.ExitCode())
	status, ok := decodeWindowsWaitStatus(0, code)
	if !ok || status.Signal() != sig.Num {
		t.Fatalf("exit code %#x decoded to %+v (ok=%v), want signal %d", code, status, ok, sig.Num)
	}
}

// suspendProcess and resumeProcess report an error for a process that is
// gone, as kill(2) does with ESRCH.
func TestSuspendProcessGone(t *testing.T) {
	tk := startTicker(t, 0)
	pid := tk.pid()
	if err := tk.cmd.Process.Kill(); err != nil {
		t.Fatalf("killing the ticker: %v", err)
	}
	_ = tk.wait()
	if err := suspendProcess(pid); err == nil {
		t.Error("suspending a dead process succeeded")
	}
	if err := resumeProcess(pid); err == nil {
		t.Error("resuming a dead process succeeded")
	}
}

// adoptTicker wires tk into r's job table as job %1, the way a `&` statement
// would: a live PID, a closed pidReady, and a goroutine that publishes the
// exit status and closes done when the process really exits.
func adoptTicker(t *testing.T, r *Runner, tk *ticker) *bgProc {
	t.Helper()
	bg := &bgProc{
		done:       make(chan struct{}),
		exit:       new(exitStatus),
		cmd:        "ticker",
		jobID:      1,
		jobControl: true,
		pidReady:   make(chan struct{}),
	}
	bg.pid.Store(int64(tk.pid()))
	close(bg.pidReady)
	go func() {
		if err := tk.wait(); err != nil {
			*bg.exit = exitStatus{code: 1}
		}
		close(bg.done)
	}()
	r.bgProcs = []*bgProc{bg}
	return bg
}

// newJobControlRunner is newJobFmtRunner with monitor mode on, which `fg`
// and `bg` both require.
func newJobControlRunner(t *testing.T) (*Runner, *bytes.Buffer) {
	t.Helper()
	r, buf := newJobFmtRunner(t, false)
	r.noOpSetState["monitor"] = true
	return r, buf
}

// `fg %1` on a stopped external job: resume it and wait for it. There is no
// terminal to hand over on Windows, so that is the whole of it — jobs.tests
// line 219 prints the job's command and then blocks until `sleep 4` ends.
func TestForegroundStoppedJob(t *testing.T) {
	tk := startTicker(t, 400)
	r, buf := newJobControlRunner(t)
	bg := adoptTicker(t, r, tk)

	waitForProgress(t, tk, 0, "before kill -STOP")
	if err := sendSignal(tk.pid(), mustSignal(t, "STOP")); err != nil {
		t.Fatalf("kill -STOP %%1: %v", err)
	}
	r.recordJobSignal(bg, mustSignal(t, "STOP"))
	stopped := holdStill(t, tk, "after kill -STOP %1")
	if !jobStoppedState(bg) {
		t.Fatal("`jobs -s` would not list the stopped job")
	}

	exit := r.builtin(context.Background(), syntax.Pos{}, "fg", []string{"%1"})
	if exit.code != 0 {
		t.Fatalf("fg %%1 exited %d", exit.code)
	}
	if got := buf.String(); got != "ticker\n" {
		t.Errorf("fg printed %q, want the job's command", got)
	}
	if tk.seen.Load() <= stopped {
		t.Error("fg returned without ever resuming the job")
	}
	select {
	case <-bg.done:
	default:
		t.Error("fg returned before the job exited")
	}
	if len(r.bgProcs) != 0 {
		t.Errorf("fg left %d finished jobs in the table", len(r.bgProcs))
	}
}

// `bg %1` on a stopped external job: resume it and leave it running, without
// waiting. jobs.tests line 200 does this to %3 and then kills it.
func TestBackgroundStoppedJob(t *testing.T) {
	tk := startTicker(t, 0)
	r, buf := newJobControlRunner(t)
	bg := adoptTicker(t, r, tk)

	waitForProgress(t, tk, 0, "before kill -STOP")
	if err := sendSignal(tk.pid(), mustSignal(t, "STOP")); err != nil {
		t.Fatalf("kill -STOP %%1: %v", err)
	}
	r.recordJobSignal(bg, mustSignal(t, "STOP"))
	stopped := holdStill(t, tk, "after kill -STOP %1")

	exit := r.builtin(context.Background(), syntax.Pos{}, "bg", []string{"%1"})
	if exit.code != 0 {
		t.Fatalf("bg %%1 exited %d", exit.code)
	}
	if got, want := buf.String(), "[1]+ ticker &\n"; got != want {
		t.Errorf("bg printed %q, want %q", got, want)
	}
	if !jobRunningState(bg) {
		t.Error("`jobs -r` would not list the resumed job")
	}
	waitForProgress(t, tk, stopped, "after bg %1")
}
