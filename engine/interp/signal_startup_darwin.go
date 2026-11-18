//go:build darwin

// Copyright (c) 2026, the outpost authors
// See LICENSE for licensing information

package interp

import (
	"syscall"
	"unsafe"
)

func startupIgnoredSignals(env string) map[string]bool {
	ignored := parseHardIgnore(env)
	for _, e := range sortedSignalEntries() {
		if e.Sig == 0 || e.Name == "KILL" || e.Name == "STOP" {
			continue
		}
		// A non-interactive Go harness may launch us with SIGINT or SIGPIPE
		// ignored for its own process plumbing. Neither disposition has
		// trustworthy shell-entry provenance here. Treat only the explicit
		// bashy bridge as a hard ignore for these two signals so embedded
		// runners and bare `trap` output are deterministic.
		if e.Name == "INT" || e.Name == "PIPE" {
			continue
		}
		var act [16]byte
		_, _, errno := syscall.RawSyscall(syscall.SYS_SIGACTION,
			uintptr(e.Sig), 0, uintptr(unsafe.Pointer(&act[0])))
		if errno != 0 {
			continue
		}
		if *(*uintptr)(unsafe.Pointer(&act[0])) == 1 {
			ignored = mergeStartupIgnored(ignored, e.Name)
		}
	}
	return ignored
}
