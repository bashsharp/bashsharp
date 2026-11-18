//go:build unix

package cli

import (
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty/v2"
)

// TestS88LoginStdinReaderPreservesChildTTY is the public process-level shape
// behind a spawned `mesg y`: a noninteractive login shell reads commands from
// fd 0 on a PTY, and the foreground provider must still observe fd 0 as a TTY.
func TestS88LoginStdinReaderPreservesChildTTY(t *testing.T) {
	cmd := exec.Command(builtBashBin(t), "--noprofile", "--norc", "--login", "+i", "-s")
	cmd.Env = []string{"HOME=" + t.TempDir(), "PATH=/bin:/usr/bin", "TERM=xterm"}

	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer ptmx.Close()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = tty, tty, tty
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	_ = tty.Close()
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	capture := startPTYCapture(ptmx)
	if _, err := ptmx.Write([]byte("tty >/dev/null && echo S88_CHILD_TTY_OK; exit\n")); err != nil {
		t.Fatal(err)
	}
	got := capture.waitFor(t, []byte("S88_CHILD_TTY_OK"), 5*time.Second)
	if err := cmd.Wait(); err != nil {
		t.Fatalf("login stdin reader: %v; output=%q", err, got)
	}
}
