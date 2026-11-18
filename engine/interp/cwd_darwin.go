// Copyright (c) 2017, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

//go:build darwin

package interp

import (
	"bytes"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

func runnerPhysicalDir(r *Runner, fallback string) string {
	if r.dirFile == nil {
		return fallback
	}
	// F_GETPATH accepts a caller-owned PATH_MAX-sized buffer. Darwin's
	// MAXPATHLEN is 1024; use a larger buffer so this remains safe if raised.
	var buf [4096]byte
	_, _, errno := syscall.Syscall(syscall.SYS_FCNTL, r.dirFile.Fd(), unix.F_GETPATH,
		uintptr(unsafe.Pointer(&buf[0])))
	if errno != 0 {
		return fallback
	}
	if end := bytes.IndexByte(buf[:], 0); end >= 0 {
		return string(buf[:end])
	}
	return fallback
}
