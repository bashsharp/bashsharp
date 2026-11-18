// Copyright (c) 2026, the outpost authors
// See LICENSE for licensing information

//go:build windows

package interp

import (
	"os"
	"syscall"
)

// relayRuntimeDefaultSignal applies the process default action for a signal
// delivered under a native-default subscription. On Windows that action is
// whatever the standalone host installed with SetProcessSignalDefault
// (exiting with the signal marker); an embedding host has none and the
// delivery is dropped.
func relayRuntimeDefaultSignal(sig os.Signal) bool {
	s, ok := sig.(syscall.Signal)
	if !ok {
		return false
	}
	return processSignalBus.runDefault(int(s))
}
