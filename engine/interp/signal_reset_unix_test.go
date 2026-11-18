// Copyright (c) 2026, the bashy authors
// See LICENSE for licensing information

//go:build unix

package interp_test

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestTrapResetRestoresOSDefault is the regression test for VSC-PCTS
// TP712's POSIX trap default-reset semantics. A standalone shell that runs
//
//	trap '' USR2   # ignore
//	trap - USR2    # reset to default
//	read x         # block
//
// must again be terminated by an externally-delivered SIGUSR2 under the
// signal's default disposition. Before the WithSignalResetter seam the
// `trap ”` SIG_IGN survived the reset (Go's signal.Reset cannot undo
// signal.Ignore), so SIGUSR2 was silently swallowed and the shell hung.
//
// The scenario is driven in a re-exec'd child that opts into
// interp.OSSignalResetter, so the real OS default kill is observed by this
// test process without taking down the test binary itself.
func TestTrapResetRestoresOSDefault(t *testing.T) {
	t.Parallel()

	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), "GOSH_CMD=sig_reset_default")

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	// Hold the write end of the child's stdin open so its `read x` blocks
	// (rather than seeing EOF and exiting cleanly before we can signal it).
	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdinW.Close()
	cmd.Stdin = stdinR

	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	stdinR.Close() // the child holds its own copy now

	// Wait for the child to announce it has reset the trap and is about to
	// block, so the signal lands under the restored default disposition.
	ready := make(chan struct{})
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			if strings.TrimSpace(sc.Text()) == "ready" {
				close(ready)
				return
			}
		}
	}()
	select {
	case <-ready:
	case <-time.After(10 * time.Second):
		cmd.Process.Kill()
		t.Fatal("child never reported ready")
	}

	if err := cmd.Process.Signal(syscall.SIGUSR2); err != nil {
		t.Fatalf("signaling child: %v", err)
	}

	// The child must be terminated by SIGUSR2, not exit normally and not
	// hang. cmd.Wait runs in a goroutine so a regression shows up as a
	// timeout rather than a stuck test.
	waitErr := make(chan error, 1)
	go func() { waitErr <- cmd.Wait() }()
	select {
	case err := <-waitErr:
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("child did not exit with an error; got %v (a swallowed signal)", err)
		}
		ws, ok := ee.Sys().(syscall.WaitStatus)
		if !ok {
			t.Fatalf("unexpected wait status type %T", ee.Sys())
		}
		if !ws.Signaled() || ws.Signal() != syscall.SIGUSR2 {
			t.Fatalf("child not terminated by SIGUSR2: signaled=%v signal=%v code=%d",
				ws.Signaled(), ws.Signal(), ws.ExitStatus())
		}
	case <-time.After(10 * time.Second):
		cmd.Process.Kill()
		t.Fatal("child hung after SIGUSR2; trap reset did not restore the OS default")
	}
}

func startStandaloneSignalDefault(t *testing.T) (*exec.Cmd, int) {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), "GOSH_CMD=sig_default")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := cmd.Process.Pid
	sc := bufio.NewScanner(stdout)
	if !sc.Scan() || strings.TrimSpace(sc.Text()) != "ready" {
		t.Fatal("standalone child never reported ready")
	}
	return cmd, pid
}

func TestStandaloneSignalDefaults(t *testing.T) {
	for _, tc := range []struct {
		name string
		sig  syscall.Signal
		stop bool
	}{
		{"ABRT", syscall.SIGABRT, false},
		{"USR1", syscall.SIGUSR1, false},
		{"TSTP", syscall.SIGTSTP, true},
		{"TTIN", syscall.SIGTTIN, true},
		{"TTOU", syscall.SIGTTOU, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd, pid := startStandaloneSignalDefault(t)
			defer func() {
				_ = syscall.Kill(pid, syscall.SIGCONT)
				_ = syscall.Kill(pid, syscall.SIGKILL)
				_, _ = cmd.Process.Wait()
			}()
			if err := syscall.Kill(pid, tc.sig); err != nil {
				t.Fatal(err)
			}
			if !tc.stop {
				waited := make(chan struct {
					state *os.ProcessState
					err   error
				}, 1)
				go func() {
					state, err := cmd.Process.Wait()
					waited <- struct {
						state *os.ProcessState
						err   error
					}{state, err}
				}()
				select {
				case result := <-waited:
					if result.err != nil {
						t.Fatal(result.err)
					}
					ws, ok := result.state.Sys().(syscall.WaitStatus)
					if !ok || !ws.Signaled() || ws.Signal() != tc.sig {
						t.Fatalf("wait status = %v, want signal %s", result.state.Sys(), tc.sig)
					}
				case <-time.After(2 * time.Second):
					t.Fatalf("child did not take the default action for %s", tc.sig)
				}
				return
			}
			waitStandaloneStopped(t, pid, tc.sig)
		})
	}
}

func TestBackgroundSelfSignalSurvivesExecReplacement(t *testing.T) {
	for _, tc := range []struct {
		name, signal, delay string
		wantSignal          syscall.Signal
		wantExit            int
		external            bool
		carrier             bool
	}{
		{name: "term", signal: "TERM", delay: "0.05", wantSignal: syscall.SIGTERM},
		{name: "term_immediate", signal: "TERM", wantSignal: syscall.SIGTERM},
		{name: "handled_term", signal: "HANDLED", delay: "0.05", wantExit: 7},
		{name: "probe", signal: "0", delay: "0.05"},
		{name: "probe_immediate", signal: "0"},
		{name: "external_term", signal: "TERM", delay: "0.05", wantSignal: syscall.SIGTERM, external: true},
		{name: "external_probe", signal: "0", delay: "0.05", external: true},
		{name: "carrier_term", signal: "TERM", delay: "0.05", wantSignal: syscall.SIGTERM, carrier: true},
		{name: "carrier_usr1", signal: "USR1", delay: "0.05", wantSignal: syscall.SIGUSR1, carrier: true},
		{name: "carrier_usr2", signal: "USR2", delay: "0.05", wantSignal: syscall.SIGUSR2, carrier: true},
		{name: "carrier_handled_term", signal: "HANDLED", delay: "0.05", wantExit: 7, carrier: true},
		{name: "carrier_probe", signal: "0", delay: "0.05", carrier: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			marker := t.TempDir() + "/kill-status"
			cmd := exec.Command(os.Args[0])
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			cmd.Env = append(os.Environ(),
				"GOSH_CMD=bg_self_signal_exec",
				"GOSH_MARKER="+marker,
				"GOSH_SIGNAL="+tc.signal,
				"GOSH_DELAY="+tc.delay,
			)
			if tc.delay == "" {
				// This row measures a signal with no delay after an actual
				// replacement has started. A separate test covers a signal
				// which wins before any exec publication.
				readyFIFO := t.TempDir() + "/replacement-ready"
				if err := syscall.Mkfifo(readyFIFO, 0o600); err != nil {
					t.Fatal(err)
				}
				cmd.Env = append(cmd.Env, "GOSH_READY_FIFO="+readyFIFO)
				cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			}
			if tc.external {
				cmd.Env = append(cmd.Env, "GOSH_EXTERNAL_KILL=1")
			}
			if tc.carrier {
				cmd.Env = append(cmd.Env, "GOSH_JOB_CARRIER=1")
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case err := <-done:
				if tc.wantSignal != 0 {
					var ee *exec.ExitError
					if !errors.As(err, &ee) {
						t.Fatalf("replacement exited normally: %v", err)
					}
					ws := ee.Sys().(syscall.WaitStatus)
					if !ws.Signaled() || ws.Signal() != tc.wantSignal {
						t.Fatalf("wait status = %v, want signal %s", ws, tc.wantSignal)
					}
				} else if tc.wantExit != 0 {
					var ee *exec.ExitError
					if !errors.As(err, &ee) || ee.ExitCode() != tc.wantExit {
						t.Fatalf("replacement exit = %v, want %d", err, tc.wantExit)
					}
				} else if err != nil {
					t.Fatalf("probe replacement: %v", err)
				}
			case <-time.After(2 * time.Second):
				if tc.delay == "" {
					_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				}
				_ = cmd.Process.Kill()
				<-done
				got, _ := os.ReadFile(marker)
				t.Fatalf("replacement did not complete; marker=%q", got)
			}
			got, err := os.ReadFile(marker)
			if err != nil {
				t.Fatalf("background job did not survive exec: %v", err)
			}
			if string(got) != "S0" {
				t.Fatalf("kill status = %q, want S0", got)
			}
		})
	}
}

func TestBackgroundSelfStopSurvivesExecReplacement(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux waitid stop relay")
	}
	marker := t.TempDir() + "/kill-status"
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(),
		"GOSH_CMD=bg_self_signal_exec",
		"GOSH_MARKER="+marker,
		"GOSH_SIGNAL=STOP",
		"GOSH_DELAY=0.05",
		"GOSH_JOB_CARRIER=1",
	)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = syscall.Kill(cmd.Process.Pid, syscall.SIGCONT)
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	stopped := make(chan syscall.WaitStatus, 1)
	go func() {
		var status syscall.WaitStatus
		_, _ = syscall.Wait4(cmd.Process.Pid, &status, syscall.WUNTRACED, nil)
		stopped <- status
	}()
	select {
	case status := <-stopped:
		if !status.Stopped() || status.StopSignal() != syscall.SIGSTOP {
			t.Fatalf("replacement proxy status = %v, want stopped by SIGSTOP", status)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("replacement proxy did not mirror child stop")
	}
	if err := syscall.Kill(cmd.Process.Pid, syscall.SIGCONT); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for {
		got, err := os.ReadFile(marker)
		if err == nil && string(got) == "S0" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("background signal marker = %q, %v; want S0", got, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestUnrelatedBackgroundDoesNotDelayExecReplacement(t *testing.T) {
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), "GOSH_CMD=bg_unrelated_exec")
	started := time.Now()
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("exec replacement waited for unrelated background job: %v", elapsed)
	}
}

func TestParentSignalReachesExecReplacement(t *testing.T) {
	marker := t.TempDir() + "/handled"
	cmd := exec.Command(os.Args[0])
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), "GOSH_CMD=proxy_external_signal", "GOSH_MARKER="+marker)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()
	ready := make([]byte, 5)
	if _, err := io.ReadFull(stdout, ready); err != nil || string(ready) != "ready" {
		t.Fatalf("replacement readiness = %q, %v", ready, err)
	}
	if err := syscall.Kill(cmd.Process.Pid, syscall.SIGUSR1); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		var ee *exec.ExitError
		if !errors.As(err, &ee) || ee.ExitCode() != 7 {
			t.Fatalf("replacement exit = %v, want 7", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("replacement did not react to parent signal")
	}
	if got, err := os.ReadFile(marker); err != nil || string(got) != "handled" {
		t.Fatalf("replacement signal marker = %q, %v", got, err)
	}
}

func waitStandaloneStopped(t *testing.T, pid int, sig syscall.Signal) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var ws syscall.WaitStatus
		got, err := syscall.Wait4(pid, &ws, syscall.WNOHANG|syscall.WUNTRACED, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got == pid {
			if !ws.Stopped() || ws.StopSignal() != sig {
				t.Fatalf("child did not stop for %s: status=%v", sig, ws)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("child did not enter the stopped state for %s", sig)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestBackgroundSelfSignalBeforeExecTerminatesOwner(t *testing.T) {
	for _, mode := range []string{"file", "incremental", "alias"} {
		t.Run(mode, func(t *testing.T) {
			cmd := exec.Command(os.Args[0])
			cmd.Env = append(os.Environ(), "GOSH_CMD=bg_self_signal_before_exec", "GOSH_OWNER_MODE="+mode)
			var output bytes.Buffer // bashpp-racegate:safe-synchronized cmd.Wait joins the pipe copier before inspection.
			cmd.Stdout, cmd.Stderr = &output, &output
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case err := <-done:
				var exited *exec.ExitError
				if !errors.As(err, &exited) {
					t.Fatalf("owner did not take its default signal: %v", err)
				}
				status := exited.Sys().(syscall.WaitStatus)
				if !status.Signaled() || status.Signal() != syscall.SIGTERM {
					t.Fatalf("owner status = %v, want SIGTERM", status)
				}
				if got := output.String(); got != "EXIT-CLEANUP\n" {
					t.Fatalf("owner EXIT cleanup = %q", got)
				}
			case <-time.After(2 * time.Second):
				_ = cmd.Process.Kill()
				<-done
				t.Fatal("owner waited for a non-exec sender's remaining work")
			}
		})
	}
}
