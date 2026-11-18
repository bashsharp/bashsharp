// Copyright (c) 2026, the outpost authors
// See LICENSE for licensing information

//go:build !unix && !windows

package interp

import "os/exec"

// execWaitStatus never reports a signal death on plan9/js: [waitStatus]
// there is the empty no-op type from os_notunix.go.
func execWaitStatus(cmd *exec.Cmd, err error) (waitStatus, bool) {
	return waitStatus{}, false
}
