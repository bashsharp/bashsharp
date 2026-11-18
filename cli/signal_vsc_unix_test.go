// Copyright (c) 2026 qiangli
// See LICENSE for licensing information

//go:build unix

package cli

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

type signalShellProcess struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	lines  <-chan string
	stderr *bytes.Buffer
}

func startSignalShell(t *testing.T, cmd *exec.Cmd) signalShellProcess {
	t.Helper()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderr := new(bytes.Buffer)
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	lines := make(chan string, 8)
	go func() {
		defer close(lines)
		s := bufio.NewScanner(stdout)
		for s.Scan() {
			lines <- s.Text()
		}
	}()
	return signalShellProcess{cmd: cmd, stdin: stdin, lines: lines, stderr: stderr}
}

func signalShell(t *testing.T, script string) signalShellProcess {
	t.Helper()
	cmd := exec.Command(builtBashBin(t), "-c", script)
	cmd.Env = []string{"PATH=/bin:/usr/bin", "HOME=" + t.TempDir()}
	return startSignalShell(t, cmd)
}

func (p signalShellProcess) wantLine(t *testing.T, want string) {
	t.Helper()
	select {
	case got, ok := <-p.lines:
		if !ok {
			t.Fatalf("stdout closed before %q; stderr:\n%s", want, p.stderr)
		}
		if got != want {
			t.Fatalf("stdout line = %q, want %q; stderr:\n%s", got, want, p.stderr)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("timeout waiting for %q; stderr:\n%s", want, p.stderr)
	}
}

func (p signalShellProcess) finish(t *testing.T) {
	t.Helper()
	_ = p.stdin.Close()
	if err := p.cmd.Wait(); err != nil {
		t.Fatalf("shell failed: %v; stderr:\n%s", err, p.stderr)
	}
}

// TestVSCTrapSignalBoundary covers the signal semantics isolated by the VSC
// sh_12 cluster. Every case crosses a real process boundary; the read cases
// deliberately deliver TERM before supplying input, matching SigWait -r.
// TP720 here pins the supported sideband consumer, not discovery: VSC SigWait
// does not set BASHY_HARD_IGNORE, so direct native-parent entry remains a
// packaging/launcher responsibility outside the Go runtime.
func TestVSCTrapSignalBoundary(t *testing.T) {
	t.Run("TP712 reset restores default", func(t *testing.T) {
		p := signalShell(t, "trap 'echo caught' TERM; kill -s TERM $$; trap - TERM; echo ready; read x; echo BAD")
		p.wantLine(t, "caught")
		p.wantLine(t, "ready")
		if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		_ = p.stdin.Close()
		err := p.cmd.Wait()
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("reset shell error = %v, want signal death; stderr:\n%s", err, p.stderr)
		}
		ws, ok := ee.Sys().(syscall.WaitStatus)
		if !ok || !ws.Signaled() || ws.Signal() != syscall.SIGTERM {
			t.Fatalf("reset shell wait status = %#v, want SIGTERM; stderr:\n%s", ee.Sys(), p.stderr)
		}
	})

	t.Run("TP713 null action ignores", func(t *testing.T) {
		p := signalShell(t, "trap '' TERM; echo ready; read x; echo got=$x")
		p.wantLine(t, "ready")
		if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(p.stdin, "input\n"); err != nil {
			t.Fatal(err)
		}
		p.wantLine(t, "got=input")
		p.finish(t)
	})

	t.Run("TP714 action traps and read resumes", func(t *testing.T) {
		p := signalShell(t, "trap 'echo caught' TERM; echo ready; read x; echo got=$x")
		p.wantLine(t, "ready")
		if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		p.wantLine(t, "caught")
		if _, err := io.WriteString(p.stdin, "input\n"); err != nil {
			t.Fatal(err)
		}
		p.wantLine(t, "got=input")
		p.finish(t)
	})

	t.Run("TP718 name needs no SIG prefix", func(t *testing.T) {
		out, status := runBuiltBash(t, t.TempDir(), "trap 'echo caught' TERM; kill -s TERM $$; echo after")
		if status != 0 || out != "caught\nafter\n" {
			t.Fatalf("status=%d output=%q, want 0 and caught/after", status, out)
		}
	})

	t.Run("TP720 sideband keeps startup ignore immutable", func(t *testing.T) {
		// /bin/sh sets SIG_IGN immediately before exec while the fixture
		// explicitly supplies the sideband. The standalone runner consumes
		// that provenance and re-applies SIG_IGN before executing script.
		const script = "trap 'echo BAD' TERM; trap - TERM; echo ready; read x; echo got=$x"
		cmd := exec.Command("/bin/sh", "-c", `trap '' TERM; exec "$BASH_BIN" -c "$BASH_SCRIPT"`)
		cmd.Env = append(os.Environ(),
			"PATH=/bin:/usr/bin",
			"BASH_BIN="+builtBashBin(t),
			"BASH_SCRIPT="+script,
			"BASHY_HARD_IGNORE=TERM",
		)
		p := startSignalShell(t, cmd)
		p.wantLine(t, "ready")
		if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(p.stdin, "input\n"); err != nil {
			t.Fatal(err)
		}
		p.wantLine(t, "got=input")
		p.finish(t)
	})
}

// GA42 sends a signal after starting a noninteractive shell but before
// providing the script on stdin. The launcher records inherited SIG_IGN in
// BASHY_HARD_IGNORE; the CLI must consume it before blocking on stdin.
func TestInheritedIgnoredSignalsWhileWaitingForStdin(t *testing.T) {
	for _, name := range []string{"ABRT", "ALRM", "PIPE", "QUIT", "TERM", "USR1", "USR2"} {
		t.Run(name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			cmd := exec.Command("/bin/sh", "-c", "trap '' "+name+`; exec "$BASH_BIN"`)
			cmd.Env = append(os.Environ(),
				"BASH_BIN="+builtBashBin(t),
				"BASHY_HARD_IGNORE="+name,
				"POSIXLY_CORRECT=1",
			)
			cmd.Stdout, cmd.Stderr = &out, &errOut
			stdin, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			// Leave stdin open and empty until after delivery, as SigWait -r
			// does. The shell remains blocked in the script read at this point.
			time.Sleep(300 * time.Millisecond)
			sig, ok := syscall.Signal(0), false
			for _, entry := range carrierSignalNumbers {
				if entry.name == name {
					sig, ok = entry.sig, true
					break
				}
			}
			if !ok {
				t.Fatalf("unknown signal %q", name)
			}
			if err := cmd.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			_, writeErr := io.WriteString(stdin, "printf survived\n")
			_ = stdin.Close()
			waitErr := cmd.Wait()
			if writeErr != nil || waitErr != nil || out.String() != "survived" {
				t.Fatalf("signal %s: write=%v wait=%v stdout=%q stderr=%q", name, writeErr, waitErr, out.String(), errOut.String())
			}
		})
	}
}
