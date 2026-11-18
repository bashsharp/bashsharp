// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

//go:build unix

package interp

import (
	"os"

	"golang.org/x/sys/unix"
)

func bashPPTaskProbeOpen(dirFile *os.File, dir, path string, flags int, mode os.FileMode) (*os.File, bool, error) {
	openFlags := flags | unix.O_NONBLOCK | unix.O_CLOEXEC
	var fd int
	var err error
	if shellPathAbs(path) {
		fd, err = unix.Open(path, openFlags, uint32(mode))
	} else if dirFile == nil {
		return nil, true, &os.PathError{Op: "open", Path: path, Err: unix.EBADF}
	} else {
		fd, err = unix.Openat(int(dirFile.Fd()), path, openFlags, uint32(mode))
	}
	if err != nil {
		// In particular, fail closed on ENXIO from a FIFO writer without a
		// reader. Path-only classification could race a FIFO replacement and
		// would arm later tasks without having acquired the intended object.
		return nil, true, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(fd), shellPathJoinAbs(dir, path)), true, nil
}

func bashPPTaskSourceClearNonblock(file *os.File) error {
	return unix.SetNonblock(int(file.Fd()), false)
}
