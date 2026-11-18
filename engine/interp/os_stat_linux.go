// Copyright (c) 2026, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

//go:build linux || solaris

package interp

import (
	"syscall"
	"time"
)

func unixStatMtime(s *syscall.Stat_t) time.Time {
	// Stat_t timespec fields are int64 on 64-bit but int32 on 32-bit
	// (e.g. GOARCH=386), so convert explicitly for time.Unix (wants int64).
	return time.Unix(int64(s.Mtim.Sec), int64(s.Mtim.Nsec))
}

func unixStatAtime(s *syscall.Stat_t) time.Time {
	return time.Unix(int64(s.Atim.Sec), int64(s.Atim.Nsec))
}
