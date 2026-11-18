// Copyright (c) 2026, the outpost authors
// See LICENSE for licensing information

//go:build windows

package interp

import "os/exec"

// waitStatus on Windows is decoded from the bashy signal marker or from the
// signal recorded by sendSignal; see signal_wintable.go.
type waitStatus = windowsWaitStatus

// execWaitStatus reports whether a reaped child died of a signal. Windows
// has no wait status, so a death is recognised either from the signal this
// shell recorded when it terminated the pid (terminatedBySignal), or from the
// bashy-owned marker exit code a bashy child exits with when it dies of a
// default-action signal. Any other exit code — including a plain `exit 143`
// — is an ordinary exit.
func execWaitStatus(cmd *exec.Cmd, err error) (waitStatus, bool) {
	ee, ok := err.(*exec.ExitError)
	if !ok || ee.ProcessState == nil {
		return waitStatus{}, false
	}
	recorded := 0
	if v, ok := terminatedBySignal.LoadAndDelete(ee.ProcessState.Pid()); ok {
		recorded = v.(int)
	}
	return decodeWindowsWaitStatus(recorded, uint32(ee.ProcessState.ExitCode()))
}
