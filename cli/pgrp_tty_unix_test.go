// Copyright (c) 2026 qiangli
// See LICENSE for licensing information

//go:build unix

package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/creack/pty/v2"
)

// TestHarnessProcessGroupKeepsTTYForeground reproduces the VSC stty/ps cap:
// script(1) owns the foreground PTY group and launches Bashy as its child,
// while BASH_SETPGRP asks Bashy to isolate non-TTY fixture trees. A bare
// setpgid here makes Bashy a background group; stty's terminal ioctl then
// stops on SIGTTOU forever. GNU Bash's initialize_job_control performs a
// setpgid only together with a terminal handoff (tcsetpgrp); a noninteractive
// script does neither. Bashy must therefore retain the wrapper's foreground
// group whenever either standard stream is a terminal.
func TestHarnessProcessGroupKeepsTTYForeground(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "bashy.pid")
	command := fmt.Sprintf(`echo $$ >%q; stty -a >/dev/null && echo STTY_OK`, pidFile)
	wrapper := exec.Command("/bin/sh", "-c", `BASH_SETPGRP=1 "$1" --noprofile --norc -c "$2"; :`, "sh", builtBashBin(t), command)
	wrapper.Env = []string{"HOME=" + dir, "PATH=/bin:/usr/bin", "TERM=xterm"}

	ptmx, err := pty.StartWithSize(wrapper, &pty.Winsize{Rows: 24, Cols: 80})
	if err != nil {
		t.Fatal(err)
	}
	defer ptmx.Close()
	capture := startPTYCapture(ptmx)
	t.Cleanup(func() {
		if data, err := os.ReadFile(pidFile); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
				_ = syscall.Kill(-pid, syscall.SIGCONT)
				_ = syscall.Kill(-pid, syscall.SIGKILL)
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
		if wrapper.ProcessState == nil {
			_ = wrapper.Process.Kill()
			_ = wrapper.Wait()
		}
	})

	got := capture.waitFor(t, []byte("STTY_OK"), 5*time.Second)
	if err := wrapper.Wait(); err != nil {
		t.Fatalf("TTY wrapper: %v; output:\n%s", err, got)
	}
}
