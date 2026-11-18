// Copyright (c) 2026, the outpost authors
// See LICENSE for licensing information

//go:build unix

package interp

import (
	"os"
	"syscall"
)

func relayRuntimeDefaultSignal(sig os.Signal) bool {
	s, ok := sig.(syscall.Signal)
	if !ok {
		return false
	}
	name, ok := signalName(s)
	if !ok || !isRuntimeSignal(name) {
		return false
	}
	restoreExecSignal(sig)
	_ = syscall.Kill(os.Getpid(), s)
	return true
}
