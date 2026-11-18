// Copyright (c) 2024, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

//go:build unix

package interp

import (
	"syscall"
	"time"
)

// processCPUTimes samples this shell process's accumulated user and system
// CPU time via getrusage(RUSAGE_SELF). RUSAGE_SELF covers the calling
// process and all its threads — which includes the goroutines that run
// builtins and simulated subshells — but not reaped external children, so
// callers add child CPU separately (see timingScope). ok is false only if
// the syscall itself fails, in which case callers must not fabricate a
// value.
func processCPUTimes() (user, sys time.Duration, ok bool) {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0, 0, false
	}
	return time.Duration(ru.Utime.Nano()), time.Duration(ru.Stime.Nano()), true
}
