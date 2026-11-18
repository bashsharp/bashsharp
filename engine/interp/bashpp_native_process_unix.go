//go:build unix

package interp

import (
	"errors"
	"os/exec"
	"syscall"
)

func bashPPNativeProcessGroup(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }
func bashPPNativeKill(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}

// bashPPProcessExitStatus maps a process's Wait error onto a bash-style exit
// status: 0 for success, the child's exit code for a normal exit, and 128+N
// for a process terminated by signal N — the same 128+signal convention bash
// reports in $? and `wait`. A non-ExitError failure (e.g. the process was
// never started) yields -1.
func bashPPProcessExitStatus(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return 128 + int(ws.Signal())
		}
		return ee.ExitCode()
	}
	return -1
}
