// Copyright (c) 2026, the outpost authors
// See LICENSE for licensing information

//go:build unix

package interp

import "os/exec"

// execWaitStatus returns the kernel wait status of a reaped child, if err is
// its *exec.ExitError. The [waitStatus] alias is syscall.WaitStatus.
func execWaitStatus(cmd *exec.Cmd, err error) (waitStatus, bool) {
	ee, ok := err.(*exec.ExitError)
	if !ok {
		return 0, false
	}
	status, ok := ee.Sys().(waitStatus)
	return status, ok
}
