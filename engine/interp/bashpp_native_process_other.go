//go:build !unix && !windows

package interp

import (
	"errors"
	"os/exec"
)

func bashPPNativeProcessGroup(cmd *exec.Cmd) {}
func bashPPNativeKill(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

// bashPPProcessExitStatus maps a process's Wait error onto an exit status: 0
// for success, the child's exit code for a normal exit, and -1 for a failure
// that carries no exit code (e.g. the process was never started). Platforms
// here (Windows, plan9, wasm) do not expose the unix signal encoding, so a
// signal-terminated child reports whatever exit code the OS surfaces.
func bashPPProcessExitStatus(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}
