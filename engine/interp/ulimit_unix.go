// Copyright (c) 2017, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

//go:build unix

package interp

import "syscall"

// nofileLimit reports the soft open-files limit (`ulimit -n`). ok is
// false when the limit is unavailable or unlimited.
func nofileLimit() (cur uint64, ok bool) {
	var rlim syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &rlim); err != nil {
		return 0, false
	}
	// RLIM_INFINITY is `0xffffffffffffffff` (uint64 max) on
	// linux and `0x7fffffffffffffff` (int64 max) on darwin/BSD.
	if uint64(rlim.Cur) == ^uint64(0) || uint64(rlim.Cur) == 1<<63-1 {
		return 0, false
	}
	return uint64(rlim.Cur), true
}
