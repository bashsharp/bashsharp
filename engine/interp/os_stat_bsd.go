// Copyright (c) 2026, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package interp

import (
	"syscall"
	"time"
)

func unixStatMtime(s *syscall.Stat_t) time.Time {
	return time.Unix(s.Mtimespec.Sec, s.Mtimespec.Nsec)
}

func unixStatAtime(s *syscall.Stat_t) time.Time {
	return time.Unix(s.Atimespec.Sec, s.Atimespec.Nsec)
}
