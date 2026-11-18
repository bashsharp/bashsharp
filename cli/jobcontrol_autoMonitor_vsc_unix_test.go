// Copyright (c) 2026 qiangli
// See LICENSE for licensing information

//go:build unix

package cli

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty/v2"
)

// TestVSCInteractiveAutoMonitorSurvivesGroupTTIN pins the VSC ps/SIGTTIN
// conformance hang: the VSC "ps" test set (SigWait -s TTIN -w ps ...) runs the
// SUT shell interactively on a real controlling terminal and expects a
// backgrounded job's terminal-stop signal to affect only that job, matching
// bash's own automatic job control for an interactive shell
// (initialize_job_control in jobs.c — no script ever runs `set -m` itself).
//
// Before the fix, bashy never auto-enabled monitor mode, so
// prepareBackgroundJobCmd never isolated a background job's process group
// from the shell's own. A stop signal sent to "process group 0" (the sender's
// own group — exactly how a job-control-aware test double or a real terminal
// driver addresses a background job) then also struck the shell itself, which
// has real SIG_DFL for SIGTTIN (restoreExecSignal) and stopped right along
// with the job it meant to probe. Nothing was left running to resume it: the
// interactive session, and the whole VSC test, wedged until the harness's own
// timeout cap — matching the observed evidence (child stopped in
// T/do_signal_stop, no forward progress) even though the job carrier stayed
// alive throughout (it always gets its own process group, unconditionally).
//
// This test reproduces the ps-shaped scenario directly: a backgrounded child
// stops itself with a process-group-directed SIGTTIN (simulating what SigWait
// / a real tty driver delivers to a background job), and the interactive
// shell must keep answering the next command immediately — proving its own
// process group was never part of the signal's target. The child is then
// resumed with SIGCONT and reaped normally, matching the resume half of the
// VSC purpose. Bounded throughout: every wait has an explicit timeout, so a
// regression fails fast instead of hanging the suite.
func TestVSCInteractiveAutoMonitorSurvivesGroupTTIN(t *testing.T) {
	dir := t.TempDir()
	sh := filepath.Join(dir, "sh")
	if err := os.Symlink(builtBashBin(t), sh); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(sh, "--noprofile", "--norc")
	cmd.Env = []string{
		"HOME=" + dir,
		"PATH=/bin:/usr/bin",
		"PS1=$ ",
		"TERM=xterm",
	}
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatal(err)
	}
	defer ptmx.Close()
	capture := startPTYCapture(ptmx)
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})

	capture.waitFor(t, []byte("$ "), 3*time.Second)
	const setPrompt = "export PS1='PS1 '"
	const promptMarker = "\r\nPS1 "
	send := func(line string) {
		t.Helper()
		if _, err := io.WriteString(ptmx, line+"\r"); err != nil {
			t.Fatal(err)
		}
	}
	// sendAndAwaitPrompt sends line and waits for the NEXT prompt to
	// reappear AFTER the point line was sent — [ptyCapture.waitFor] alone
	// matches anywhere in the whole accumulated buffer, including a prompt
	// left over from the previous command, so it would return immediately
	// without ever letting this command run. Waiting from an offset
	// guarantees the full round trip (input echo, execution, output) has
	// landed before the caller inspects the buffer.
	sendAndAwaitPrompt := func(line string, timeout time.Duration) []byte {
		t.Helper()
		offset := capture.len()
		send(line)
		return capture.waitForFrom(t, offset, []byte(promptMarker), timeout)
	}
	send(setPrompt)
	capture.waitFor(t, []byte(setPrompt+promptMarker), 3*time.Second)

	// The auto-monitor fix itself: an interactive shell on a real controlling
	// terminal must show job control on in `$-` without any `set -m`.
	got := sendAndAwaitPrompt("echo MON=$-", 3*time.Second)
	if !bytesContainsMonitorFlag(got) {
		t.Fatalf("interactive shell did not auto-enable monitor mode ($-); output:\n%s", got)
	}

	// Background a child that stops its own process group with SIGTTIN
	// (pid 0 in kill(1)/kill(2) means "the sender's own process group" —
	// exactly how a backgrounded job addresses itself when simulating what a
	// terminal driver, or the VSC SigWait helper, delivers to a background
	// job) before announcing it survived, then echo a marker on resume. `$!`
	// resolves to the job carrier's own PID (see docs/one-agent-control.md),
	// not the real child's — the carrier is never itself signaled or
	// stopped, so the real PID is captured separately via a PID file for the
	// stat/resume checks below, matching how an external observer (SigWait,
	// a `ps` snapshot) would locate the real process.
	pidFile := filepath.Join(dir, "child.pid")
	got = sendAndAwaitPrompt(`sh -c 'echo $$ > `+pidFile+`; kill -TTIN 0; echo CHILD_RESUMED' & echo CPID=$!`, 3*time.Second)
	cpid := extractAfter(t, got, "CPID=")

	// The shell must keep answering immediately: if its own process group
	// were not isolated from the job's, the group-directed SIGTTIN above
	// would have stopped the shell too and this would time out.
	sendAndAwaitPrompt("echo STILL_ALIVE", 5*time.Second)

	// Poll (bounded) for the real child PID to appear, then for it to
	// actually reach the stopped state.
	var realpid string
	deadline := time.Now().Add(5 * time.Second)
	for {
		b, err := os.ReadFile(pidFile)
		if err == nil && len(bytes.TrimSpace(b)) > 0 {
			realpid = string(bytes.TrimSpace(b))
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("backgrounded child never wrote its PID to %s", pidFile)
		}
		time.Sleep(20 * time.Millisecond)
	}

	got = sendAndAwaitPrompt(`i=0; while [ $i -lt 100 ]; do st=$(ps -o stat= -p `+realpid+` 2>/dev/null); case "$st" in *T*) break;; esac; i=$((i+1)); sleep 0.05; done; echo STOPSTATE="$st"`, 8*time.Second)
	if !containsStoppedState(got, "STOPSTATE=") {
		t.Fatalf("backgrounded child (real pid %s) never reached the T (stopped) state; PTY output:\n%s", realpid, got)
	}

	// Resume the real, stopped process (not the carrier, which was never
	// stopped and has no way to relay a CONT to it), then wait on the
	// carrier-tracked job: reapCarrier only fires once the real job
	// (including this now-resumed child) has actually finished, so a
	// successful, prompt `wait` here also proves the job was never torn down
	// by the context-cancellation bug this test guards against.
	got = sendAndAwaitPrompt(`kill -CONT `+realpid+`; wait `+cpid+`; echo JOBDONE=$?`, 5*time.Second)
	if !containsMarker(got, "CHILD_RESUMED") || !containsMarker(got, "JOBDONE=0") {
		t.Fatalf("backgrounded child did not resume and reap cleanly; PTY output:\n%s", got)
	}

	send("exit")
	if err := cmd.Wait(); err != nil {
		t.Fatalf("interactive shell exit: %v", err)
	}
}

func (c *ptyCapture) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.Len()
}

// waitForFrom is [ptyCapture.waitFor] restricted to the buffer captured
// after offset, so a marker left over from an earlier round trip cannot
// satisfy a wait meant for the next one.
func (c *ptyCapture) waitForFrom(t *testing.T, offset int, want []byte, timeout time.Duration) []byte {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		c.mu.Lock()
		full := c.buf.Bytes()
		var tail []byte
		if offset < len(full) {
			tail = append([]byte(nil), full[offset:]...)
		}
		c.mu.Unlock()
		if bytes.Contains(tail, want) {
			return tail
		}
		select {
		case <-c.notify:
		case <-timer.C:
			t.Fatalf("PTY output never contained %q after offset %d; got %q", want, offset, tail)
		}
	}
}

func containsMarker(got []byte, marker string) bool {
	return strings.Contains(string(got), marker)
}

// bytesContainsMonitorFlag reports whether the last "MON=" line in got
// includes the 'm' job-control flag bash prints in $-.
func bytesContainsMonitorFlag(got []byte) bool {
	s := string(got)
	idx := lastIndex(s, "MON=")
	if idx < 0 {
		return false
	}
	rest := s[idx+len("MON="):]
	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case 'm':
			return true
		case '\r', '\n':
			return false
		}
	}
	return false
}

func lastIndex(s, substr string) int {
	last := -1
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			last = i
		}
	}
	return last
}

// extractAfter returns the token right after the last occurrence of marker in
// got, stopping at the first CR/LF/space.
func extractAfter(t *testing.T, got []byte, marker string) string {
	t.Helper()
	s := string(got)
	idx := lastIndex(s, marker)
	if idx < 0 {
		t.Fatalf("marker %q not found in PTY output:\n%s", marker, got)
	}
	rest := s[idx+len(marker):]
	end := len(rest)
	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case '\r', '\n', ' ':
			end = i
		}
		if end != len(rest) {
			break
		}
	}
	tok := rest[:end]
	if tok == "" {
		t.Fatalf("empty token after marker %q in PTY output:\n%s", marker, got)
	}
	return tok
}

// containsStoppedState reports whether the STOPSTATE= line captured a real
// job-control stop (a "T" in the ps stat field).
func containsStoppedState(got []byte, marker string) bool {
	s := string(got)
	idx := lastIndex(s, marker)
	if idx < 0 {
		return false
	}
	rest := s[idx+len(marker):]
	for i := 0; i < len(rest); i++ {
		if rest[i] == '\r' || rest[i] == '\n' {
			rest = rest[:i]
			break
		}
	}
	for i := 0; i < len(rest); i++ {
		if rest[i] == 'T' {
			return true
		}
	}
	return false
}
