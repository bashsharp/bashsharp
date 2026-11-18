//go:build unix

package cli

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty/v2"
)

var (
	bashyBinOnce sync.Once
	bashyBinPath string
	bashyBinErr  error
)

func builtBashyBin(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("interactive subprocess tests build cmd/bashy; skipped with -short")
	}
	bashyBinOnce.Do(func() {
		dir, err := os.MkdirTemp("", "bashy-readline-bin-")
		if err != nil {
			bashyBinErr = err
			return
		}
		testCleanups = append(testCleanups, func() { _ = os.RemoveAll(dir) })
		bashyBinPath = filepath.Join(dir, "bashy")
		out, err := exec.Command("go", "build", "-o", bashyBinPath, "github.com/qiangli/bashy/cmd/bashy").CombinedOutput()
		if err != nil {
			bashyBinErr = fmt.Errorf("building cmd/bashy: %v\n%s", err, out)
		}
	})
	if bashyBinErr != nil {
		t.Fatal(bashyBinErr)
	}
	return bashyBinPath
}

func TestInteractiveBashPPLiveDialect(t *testing.T) {
	tests := []struct {
		name      string
		initial   string
		writes    []string
		want      string
		wantError bool
	}{
		{name: "initial-off enable same-line", initial: "0", writes: []string{"set -o bashpp; type T int; var x = 7; echo ENABLE_SAME:$x"}, want: "ENABLE_SAME:7"},
		{name: "initial-off enable multiline", initial: "0", writes: []string{"set -o bashpp", "type T int", "var x = 8", "echo ENABLE_MULTI:$x"}, want: "ENABLE_MULTI:8"},
		{name: "initial-on disable same-line", initial: "1", writes: []string{"set +o bashpp; type T int; echo DISABLE_SAME"}, want: "DISABLE_SAME", wantError: true},
		{name: "initial-on disable multiline", initial: "1", writes: []string{"set +o bashpp", "type T int", "echo DISABLE_MULTI"}, want: "DISABLE_MULTI", wantError: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command(builtBashyBin(t), "--noprofile", "--norc")
			cmd.Env = []string{"BASHY_BASHPP=" + tt.initial, "HOME=" + t.TempDir(), "PATH=/bin:/usr/bin", "PS1=LIVE> ", "TERM=xterm"}
			ptmx, err := pty.Start(cmd)
			if err != nil {
				t.Fatal(err)
			}
			capture := startPTYCapture(ptmx)
			t.Cleanup(func() {
				_ = ptmx.Close()
				if cmd.ProcessState == nil {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}
			})

			capture.waitFor(t, []byte("LIVE> "), 5*time.Second)
			for i, line := range tt.writes {
				if _, err := io.WriteString(ptmx, line+"\r"); err != nil {
					t.Fatal(err)
				}
				capture.waitForCount(t, []byte("LIVE> "), i+2, 5*time.Second)
			}

			capture.mu.Lock()
			got := append([]byte(nil), capture.buf.Bytes()...)
			capture.mu.Unlock()
			if !bytes.Contains(got, []byte("\r\n"+tt.want+"\r\n")) {
				t.Fatalf("missing executed output %q in PTY transcript: %q", tt.want, got)
			}
			hasError := bytes.Contains(got, []byte("type: T: not found"))
			if hasError != tt.wantError {
				t.Fatalf("classic type error = %v, want %v; PTY transcript: %q", hasError, tt.wantError, got)
			}

			if _, err := io.WriteString(ptmx, "exit\r"); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("interactive shell exit: %v; PTY transcript: %q", err, got)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("interactive shell did not exit; possible readline goroutine leak")
			}
		})
	}
}

func TestInteractiveBashPPPOSIXGrammarContract(t *testing.T) {
	// Exercise both the normal readline front door and sh's cooked-terminal
	// fallback. Both must consult the effective POSIX language, including a
	// live toggle which changes the latent dialect inside the same input line.
	for _, plainTerminal := range []bool{false, true} {
		name := "readline"
		if plainTerminal {
			name = "plain-terminal"
		}
		t.Run(name, func(t *testing.T) {
			binary := builtBashyBin(t)
			pathDir := t.TempDir()
			// Strict sh requires regular builtins to resolve in PATH. Provide
			// type's lookup entry so this probe measures its parsed arguments,
			// rather than the unrelated strict-POSIX PATH gate.
			if err := os.WriteFile(filepath.Join(pathDir, "type"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
				t.Fatal(err)
			}
			if plainTerminal {
				link := filepath.Join(t.TempDir(), "sh")
				if err := os.Symlink(binary, link); err != nil {
					t.Fatal(err)
				}
				binary = link
			}
			cmd := exec.Command(binary, "--noprofile", "--norc", "--posix")
			cmd.Env = []string{
				"BASHY_BASHPP=1", "HOME=" + t.TempDir(), "PATH=" + pathDir + ":/bin:/usr/bin",
				"PS1=POSIX> ", "TERM=xterm", "LC_ALL=C",
			}
			ptmx, err := pty.Start(cmd)
			if err != nil {
				t.Fatal(err)
			}
			capture := startPTYCapture(ptmx)
			t.Cleanup(func() {
				_ = ptmx.Close()
				if cmd.ProcessState == nil {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}
			})
			capture.waitFor(t, []byte("POSIX> "), 5*time.Second)
			for i, line := range []string{
				`a=(one two); printf 'ARRAY:%s\n' "${a[1]}"`,
				`set -o bashpp; type Hidden int; printf 'POSIX_BOUNDARY\n'`,
				`agentic func f(n int) int { return n }`,
				`set +o posix; type Visible int; var x = 8; printf 'ACTIVATED:%s\n' "$x"`,
				`set -o posix; type HiddenAgain int; printf 'POSIX_REENABLED\n'`,
			} {
				if _, err := io.WriteString(ptmx, line+"\r"); err != nil {
					t.Fatal(err)
				}
				capture.waitForCount(t, []byte("POSIX> "), i+2, 5*time.Second)
			}
			capture.mu.Lock()
			got := append([]byte(nil), capture.buf.Bytes()...)
			capture.mu.Unlock()
			for _, want := range []string{"ARRAY:two", "POSIX_BOUNDARY", "ACTIVATED:8", "POSIX_REENABLED"} {
				if !bytes.Contains(got, []byte("\r\n"+want+"\r\n")) {
					t.Fatalf("missing executed output %q in PTY transcript: %q", want, got)
				}
			}
			for _, want := range []string{"type: Hidden: not found", "type: HiddenAgain: not found"} {
				if !bytes.Contains(got, []byte(want)) {
					t.Fatalf("POSIX selected extended type grammar; missing %q: %q", want, got)
				}
			}
			if bytes.Contains(got, []byte("extensions disabled")) {
				t.Fatalf("Bash++ reached runtime while POSIX grammar should reject it: %q", got)
			}
			if _, err := io.WriteString(ptmx, "exit 0\r"); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("interactive shell exit: %v; PTY transcript: %q", err, got)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("interactive shell did not exit")
			}
		})
	}
}
