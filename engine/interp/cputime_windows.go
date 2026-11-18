// Copyright (c) 2024, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

//go:build windows

package interp

import (
	"syscall"
	"time"
)

// processCPUTimes samples this shell process's accumulated kernel (system)
// and user CPU time via GetProcessTimes on the current process. The
// creation and exit FILETIMEs are ignored. ok is false only if the syscall
// fails, in which case callers must not fabricate a value.
func processCPUTimes() (user, sys time.Duration, ok bool) {
	handle, err := syscall.GetCurrentProcess()
	if err != nil {
		return 0, 0, false
	}
	var creation, exit, kernel, userFT syscall.Filetime
	if err := syscall.GetProcessTimes(handle, &creation, &exit, &kernel, &userFT); err != nil {
		return 0, 0, false
	}
	return time.Duration(userFT.Nanoseconds()), time.Duration(kernel.Nanoseconds()), true
}
