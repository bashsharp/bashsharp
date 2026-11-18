// Copyright (c) 2026, the outpost authors
// See LICENSE for licensing information

//go:build windows

package interp

import (
	"os"
	"syscall"
)

// signalBusActive: on Windows the bus is the only delivery path for a
// syscall.Signal(n), so every os/signal call in signal.go is mirrored here.
const signalBusActive = true

func busNotify(ch chan<- os.Signal, sig os.Signal) {
	if s, ok := sig.(syscall.Signal); ok {
		processSignalBus.subscribe(ch, int(s), sig)
	}
}

func busStop(ch chan<- os.Signal) { processSignalBus.unsubscribe(ch) }

func busIgnore(sig os.Signal) {
	if s, ok := sig.(syscall.Signal); ok {
		processSignalBus.ignore(int(s))
	}
}

func busReset(sig os.Signal) {
	if s, ok := sig.(syscall.Signal); ok {
		processSignalBus.reset(int(s))
	}
}
