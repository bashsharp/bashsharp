// Copyright (c) 2026, the bashy authors
// See LICENSE for licensing information

//go:build unix

package interp_test

import (
	"os"
	"syscall"
)

// carrierExitSignal maps a reaped carrier's process state to the number
// of the signal that terminated it, or 0 for a normal exit.
func carrierExitSignal(ps *os.ProcessState) int {
	if ws, ok := ps.Sys().(syscall.WaitStatus); ok {
		if ws.Signaled() {
			return int(ws.Signal())
		}
		if code := ws.ExitStatus(); code > 128 && code < 256 {
			return code - 128
		}
	}
	return 0
}

// pidLive reports whether pid still exists (including as a zombie),
// probing with the null signal like `kill -0`.
func pidLive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}
