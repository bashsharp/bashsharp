// Copyright (c) 2026 qiangli
// See LICENSE for licensing information

//go:build unix

package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/interp"
)

// These tests drive the real bin/bash binary end to end: the OS-backed job
// carrier re-execs the shell executable in helper mode, so only a subprocess
// run proves the whole chain — helper interception, real `$!` PIDs, external
// signaling, and carrier reaping.

var _ interp.ProcessGroupCarrierProcess = (*execCarrierProc)(nil)

var (
	bashBinOnce sync.Once
	bashBinPath string
	bashBinErr  error
)

// builtBashBin builds cmd/bash once for the whole package and returns its path.
func builtBashBin(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("carrier subprocess tests build cmd/bash; skipped with -short")
	}
	bashBinOnce.Do(func() {
		dir, err := os.MkdirTemp("", "bashy-carrier-bin-")
		if err != nil {
			bashBinErr = err
			return
		}
		testCleanups = append(testCleanups, func() { os.RemoveAll(dir) })
		bashBinPath = filepath.Join(dir, "bash")
		out, err := exec.Command("go", "build", "-o", bashBinPath, "github.com/qiangli/bashy/cmd/bash").CombinedOutput()
		if err != nil {
			bashBinErr = fmt.Errorf("building cmd/bash: %v\n%s", err, out)
		}
	})
	if bashBinErr != nil {
		t.Fatal(bashBinErr)
	}
	return bashBinPath
}

// killBinPrelude resolves the external kill binary into $K, bypassing the
// builtin, so scripts exercise the "an external process signals the job" path.
const killBinPrelude = `K=/bin/kill; [ -x "$K" ] || K=/usr/bin/kill` + "\n"

// runBuiltBash runs `bin/bash -c script` in dir with a minimal environment and
// returns its combined output and exit code. A wedged run is killed at 30s.
func runBuiltBash(t *testing.T, dir, script string) (string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, builtBashBin(t), "-c", script)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=/bin:/usr/bin", "HOME=" + dir, "TMPDIR=" + dir}
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("script wedged (30s):\n%s", out)
	}
	exit := 0
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("running script: %v\n%s", err, out)
		}
		exit = ee.ExitCode()
	}
	return string(out), exit
}

var numericPidRe = regexp.MustCompile(`^[1-9][0-9]*$`)
var carrierPipelineIdentityRe = regexp.MustCompile(`(?:LEFT|RIGHT) pid=([0-9]+) pgrp=([0-9]+)`)

func TestCarrierOwnsMonitoredPipelineProcessGroup(t *testing.T) {
	out, exit := runBuiltBash(t, t.TempDir(), `
set -m
/bin/sh -c 'p=$$; g=$(/bin/ps -o pgid= -p "$p" | tr -d " "); echo "LEFT pid=$p pgrp=$g" >&2; sleep 1' |
  /bin/sh -c 'p=$$; g=$(/bin/ps -o pgid= -p "$p" | tr -d " "); echo "RIGHT pid=$p pgrp=$g" >&2; sleep 1' &
j=$(jobs -p)
wait
echo "JOBS_P=$j"
`)
	if exit != 0 {
		t.Fatalf("pipeline exit=%d; output:\n%s", exit, out)
	}
	job := regexp.MustCompile(`(?m)^JOBS_P=([0-9]+)$`).FindStringSubmatch(out)
	if job == nil {
		t.Fatalf("jobs -p identity missing:\n%s", out)
	}
	matches := carrierPipelineIdentityRe.FindAllStringSubmatch(out, -1)
	if len(matches) != 2 {
		t.Fatalf("pipeline child identities missing:\n%s", out)
	}
	for _, match := range matches {
		if match[2] != job[1] {
			t.Fatalf("child pid %s joined pgrp %s, want stable job group %s:\n%s", match[1], match[2], job[1], out)
		}
	}
}

func pidLive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// waitPidsGone polls until none of the PIDs exist anymore, failing the test if
// any survives the deadline — the carrier-leak check.
func waitPidsGone(t *testing.T, pids []int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for _, pid := range pids {
		for pidLive(pid) {
			if time.Now().After(deadline) {
				t.Fatalf("carrier pid %d still alive; leaked?", pid)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

// outPid extracts the `pid=N` line and asserts it is a real numeric PID.
func outPid(t *testing.T, out string) int {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if p, ok := strings.CutPrefix(line, "pid="); ok {
			if !numericPidRe.MatchString(p) {
				t.Fatalf("$! = %q, want a real numeric PID; output:\n%s", p, out)
			}
			var pid int
			fmt.Sscanf(p, "%d", &pid)
			return pid
		}
	}
	t.Fatalf("no pid= line in output:\n%s", out)
	return 0
}

// TestCarrierCertificationShape runs the exact shape that invalidated the VSC
// certification run (tetapi.sh stores $! in tet_context and does integer
// comparisons on it): a bounded pure-builtin job whose $! must be numeric,
// probeable by the external /bin/kill, and resolvable by wait.
func TestCarrierCertificationShape(t *testing.T) {
	out, exit := runBuiltBash(t, t.TempDir(),
		"tet_context=`(:)& echo $!`; case $tet_context in *[!0-9]*|\"\") exit 90;; esac; test \"$tet_context\" -gt 0")
	if exit == 90 {
		t.Fatalf("$! was not numeric (synthetic g<N>?); output:\n%s", out)
	}
	if exit != 0 {
		t.Fatalf("exit = %d, want 0; output:\n%s", exit, out)
	}
}

func TestCarrierHelperPreservesIgnoredTerm(t *testing.T) {
	restoreTerm := func() {
		// Reset leaves a kernel-level SIG_IGN in place when the signal was
		// ignored. Notify+Stop installs and then removes the Go handler, which
		// restores SIG_DFL for subsequently exec'd children and runners.
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGTERM)
		signal.Stop(ch)
	}
	defer restoreTerm()
	signal.Ignore(syscall.SIGTERM)
	cp, err := (execJobCarrier{}).StartCarrier(context.Background())
	if err != nil {
		restoreTerm()
		t.Fatal(err)
	}
	// Restore the test process immediately. The helper must retain the ignored
	// disposition it inherited across exec.
	restoreTerm()
	if err := syscall.Kill(cp.Pid(), syscall.SIGTERM); err != nil {
		cp.Terminate()
		_ = cp.Wait()
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if err := syscall.Kill(cp.Pid(), 0); err != nil {
		cp.Terminate()
		_ = cp.Wait()
		t.Fatalf("helper did not preserve inherited SIG_IGN: %v", err)
	}
	cp.Terminate()
	_ = cp.Wait()
}

// A background compound command is represented by its carrier PID. Signals
// ignored by the shell must therefore remain ignored by that carrier, just as
// they are for a directly executed command.
func TestCarrierBackgroundCompoundPreservesIgnoredTerm(t *testing.T) {
	out, exit := runBuiltBash(t, t.TempDir(), killBinPrelude+`
trap '' TERM
{ until [ -e go ]; do :; done; } &
p=$!
echo "pid=$p"
"$K" -TERM "$p"
sleep 0.05
if "$K" -0 "$p" 2>/dev/null; then echo alive; else echo dead; fi
: >go
wait "$p"
`)
	if exit != 0 || !strings.Contains(out, "alive") || strings.Contains(out, "dead") {
		t.Fatalf("exit=%d, ignored TERM did not preserve carrier liveness:\n%s", exit, out)
	}
	waitPidsGone(t, []int{outPid(t, out)})
}

// Go's runtime installs a notify-only handler for SIGUSR1 before main. A bare
// signal.Reset does not replace that handler unless os/signal first enabled
// the signal, so the carrier used to swallow USR1 and leave `wait $!` wedged.
func TestCarrierHelperRestoresDefaultUSR1(t *testing.T) {
	// Standalone runner construction leaves Go's ignore bookkeeping set
	// while restoring the kernel default. Reproduce that exact boundary,
	// which whole-package tests reach before this helper compatibility case.
	if runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
		signal.Ignore(syscall.SIGUSR1)
		(interp.OSSignalResetter{}).ResetDefault(int(syscall.SIGUSR1), "USR1")
		t.Cleanup(func() {
			ch := make(chan os.Signal, 1)
			signal.Notify(ch, syscall.SIGUSR1)
			signal.Stop(ch)
		})
	}
	cp, err := (execJobCarrier{}).StartCarrier(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer cp.Terminate()
	if err := syscall.Kill(cp.Pid(), syscall.SIGUSR1); err != nil {
		t.Fatal(err)
	}
	done := make(chan int, 1)
	go func() { done <- cp.Wait() }()
	select {
	case got := <-done:
		if got != int(syscall.SIGUSR1) {
			t.Fatalf("helper relay = %d, want SIGUSR1", got)
		}
	case <-time.After(3 * time.Second):
		cp.Terminate()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("legacy waiter did not finish after termination")
		}
		t.Fatal("kernel-default USR1 was discarded as a stale Go ignore")
	}
	waitPidsGone(t, []int{cp.Pid()})
}

// TestCarrierLivePidProbeAndCleanup makes the liveness probe deterministic: a
// file-gated pure-builtin job is held alive until the external kill -0 has
// seen its carrier, then released; afterwards the carrier must be gone.
func TestCarrierLivePidProbeAndCleanup(t *testing.T) {
	out, exit := runBuiltBash(t, t.TempDir(), killBinPrelude+`
{ : >ready; until [ -e go ]; do :; done; } &
p=$!
echo "pid=$p"
while [ ! -e ready ]; do :; done
"$K" -0 "$p" && echo alive
: >go
wait "$p"
echo "st=$?"
`)
	if exit != 0 || !strings.Contains(out, "alive") || !strings.Contains(out, "st=0") {
		t.Fatalf("exit=%d, unexpected output:\n%s", exit, out)
	}
	waitPidsGone(t, []int{outPid(t, out)})
}

// TestCarrierExternalTermAndKill pins the 128+signal mapping for an external
// signal aimed at a pure-builtin async compound: TERM -> 143, and the
// uncatchable KILL -> 137. The carrier must be reaped afterwards.
func TestCarrierExternalTermAndKill(t *testing.T) {
	for _, tc := range []struct{ sig, want string }{
		{"TERM", "143"},
		{"KILL", "137"},
	} {
		t.Run(tc.sig, func(t *testing.T) {
			out, exit := runBuiltBash(t, t.TempDir(), killBinPrelude+`
{ while :; do :; done; } &
p=$!
echo "pid=$p"
"$K" -`+tc.sig+` "$p"
wait "$p"
echo "st=$?"
`)
			if exit != 0 || !strings.Contains(out, "st="+tc.want) {
				t.Fatalf("exit=%d, want st=%s in output:\n%s", exit, tc.want, out)
			}
			waitPidsGone(t, []int{outPid(t, out)})
		})
	}
}

// TestCarrierExitsOnNaturalCompletion checks that a job finishing on its own
// reaps its carrier: after wait resolves, the carrier PID no longer exists —
// across several back-to-back jobs, each with a distinct real PID.
func TestCarrierExitsOnNaturalCompletion(t *testing.T) {
	out, exit := runBuiltBash(t, t.TempDir(), `
for i in 1 2 3 4; do
	{ :; } &
	echo "pid=$!"
	wait "$!" || exit 91
done
`)
	if exit != 0 {
		t.Fatalf("exit=%d, output:\n%s", exit, out)
	}
	var pids []int
	seen := make(map[string]bool)
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		p, ok := strings.CutPrefix(line, "pid=")
		if !ok {
			continue
		}
		if !numericPidRe.MatchString(p) {
			t.Fatalf("$! = %q, want a real numeric PID; output:\n%s", p, out)
		}
		if seen[p] {
			t.Fatalf("duplicate carrier pid %s; output:\n%s", p, out)
		}
		seen[p] = true
		var pid int
		fmt.Sscanf(p, "%d", &pid)
		pids = append(pids, pid)
	}
	if len(pids) != 4 {
		t.Fatalf("want 4 pids, got %d; output:\n%s", len(pids), out)
	}
	waitPidsGone(t, pids)
}

// TestCarrierHelperLifecycle drives helper mode directly: it must produce no
// output, survive while the parent holds its stdin pipe, exit 0 on EOF, and —
// relay an external SIGTERM as its signal number.
func TestCarrierHelperLifecycle(t *testing.T) {
	t.Run("EOF", func(t *testing.T) {
		cmd := exec.Command(builtBashBin(t), "--bashy-job-carrier")
		stdin, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		var out strings.Builder
		cmd.Stdout, cmd.Stderr = &out, &out
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(100 * time.Millisecond)
		if !pidLive(cmd.Process.Pid) {
			t.Fatal("helper exited while its stdin pipe was still open")
		}
		stdin.Close()
		if err := cmd.Wait(); err != nil {
			t.Fatalf("helper exit on EOF: %v; output: %s", err, out.String())
		}
		if out.String() != "" {
			t.Fatalf("helper produced output: %q", out.String())
		}
	})
	t.Run("Signal", func(t *testing.T) {
		cp, err := (execJobCarrier{}).StartCarrier(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if err := syscall.Kill(cp.Pid(), syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		got := cp.Wait()
		if got != int(syscall.SIGTERM) {
			t.Fatalf("helper relay = %d, want SIGTERM", got)
		}
	})
}

// TestCarrierWaitStateStop proves execCarrierProc satisfies
// interp.StopAwareCarrierProcess and accurately reports stopped and terminal states.
func TestCarrierWaitStateStop(t *testing.T) {
	cp, err := (execJobCarrier{}).StartCarrier(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer cp.Terminate()

	sa, ok := cp.(interp.StopAwareCarrierProcess)
	if !ok {
		t.Fatal("execCarrierProc does not implement interp.StopAwareCarrierProcess")
	}

	if err := syscall.Kill(sa.Pid(), syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}

	st := sa.WaitState()
	if !st.Stopped {
		t.Fatalf("WaitState() returned st=%+v, want Stopped=true", st)
	}
	if st.Signal != int(syscall.SIGSTOP) && st.Signal != int(syscall.SIGTSTP) {
		t.Fatalf("WaitState() signal = %d, want SIGSTOP (%d) or SIGTSTP (%d)", st.Signal, syscall.SIGSTOP, syscall.SIGTSTP)
	}

	sa.Terminate()
	termSt := sa.WaitState()
	if termSt.Stopped {
		t.Fatalf("WaitState() after Terminate stopped = %v, want false", termSt.Stopped)
	}
	waitPidsGone(t, []int{sa.Pid()})
}

// TestCarrierExternalStop verifies end-to-end handling of a stopped job carrier process.
func TestCarrierExternalStop(t *testing.T) {
	for _, sig := range []struct {
		name string
	}{
		{"STOP"},
		{"TSTP"},
	} {
		t.Run(sig.name, func(t *testing.T) {
			wantSt := fmt.Sprintf("st=%d", 128+int(syscall.SIGSTOP))
			out, exit := runBuiltBash(t, t.TempDir(), killBinPrelude+`
{ while :; do :; done; } &
p=$!
echo "pid=$p"
"$K" -`+sig.name+` "$p"
wait "$p"
echo "st=$?"
`)
			if exit != 0 || !strings.Contains(out, wantSt) {
				t.Fatalf("exit=%d, want %s in output:\n%s", exit, wantSt, out)
			}
			waitPidsGone(t, []int{outPid(t, out)})
		})
	}
}
