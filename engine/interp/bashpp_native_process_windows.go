//go:build windows

package interp

import (
	"errors"
	"os/exec"
	"syscall"
)

// Give the native helper its own console process group so a break addressed to
// the Bashy group reaches the shell first. The shell then forwards it to the
// helper; both processes must not receive the same event independently.
func bashPPNativeProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

func bashPPNativeKill(cmd *exec.Cmd) {
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}

func bashPPProcessExitStatus(err error) int {
	if err == nil {
		return 0
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	return -1
}
