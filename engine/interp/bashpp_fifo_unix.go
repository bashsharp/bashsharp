// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

//go:build unix

package interp

import (
	"context"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// bashPPFIFOIdentify reports whether path names a FIFO, and which inode,
// without opening it. A FIFO is reported with an error only when ctx ends.
func bashPPFIFOIdentify(ctx context.Context, dirFile *os.File, path string) (key bashPPFIFOIdentity, fifo bool, err error) {
	dirFD := unix.AT_FDCWD
	if !shellPathAbs(path) {
		if dirFile == nil {
			return key, false, nil
		}
		dirFD = int(dirFile.Fd())
	}
	var st unix.Stat_t
	if err := bashPPFIFORetry(ctx, func() error { return unix.Fstatat(dirFD, path, &st, 0) }); err != nil || st.Mode&unix.S_IFMT != unix.S_IFIFO {
		if ctx.Err() != nil {
			return key, true, ctx.Err()
		}
		return key, false, nil
	}
	return bashPPFIFOIdentity{uint64(st.Dev), uint64(st.Ino)}, true, nil
}

// bashPPFIFOFileIdentity is bashPPFIFOIdentify for an already open
// descriptor, such as one Bash's blocking open handed back.
func bashPPFIFOFileIdentity(file *os.File) (key bashPPFIFOIdentity, fifo bool) {
	var st unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFIFO {
		return key, false
	}
	return bashPPFIFOIdentity{uint64(st.Dev), uint64(st.Ino)}, true
}

// Acquire without ever entering a blocking open. The read probe pins the
// inode and keeps writer acquisition nonblocking, but is not a registered
// reader and therefore cannot itself satisfy the interpreter rendezvous.
func bashPPFIFOAcquire(ctx context.Context, dirFile *os.File, dir, path string, flags int, key bashPPFIFOIdentity) (file, probe *os.File, err error) {
	dirFD := unix.AT_FDCWD
	if !shellPathAbs(path) {
		dirFD = int(dirFile.Fd())
	}
	open := func(mode int) (*os.File, error) {
		var fd int
		err := bashPPFIFORetry(ctx, func() (err error) {
			fd, err = unix.Openat(dirFD, path, mode|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
			return err
		})
		if err != nil {
			return nil, err
		}
		var st unix.Stat_t
		if err = bashPPFIFORetry(ctx, func() error { return unix.Fstat(fd, &st) }); err != nil || st.Mode&unix.S_IFMT != unix.S_IFIFO || uint64(st.Dev) != key.dev || uint64(st.Ino) != key.ino {
			_ = unix.Close(fd)
			if err == nil {
				err = fmt.Errorf("FIFO changed while acquiring its rendezvous descriptor")
			}
			return nil, err
		}
		return os.NewFile(uintptr(fd), shellPathJoinAbs(dir, path)), nil
	}
	probe, err = open(unix.O_RDONLY)
	if err != nil {
		return nil, nil, fmt.Errorf("FIFO rendezvous requires a readable inode: %w", err)
	}
	if flags&(os.O_WRONLY|os.O_RDWR) == 0 {
		return probe, nil, nil
	}
	file, err = open(flags & (os.O_WRONLY | os.O_RDWR | os.O_APPEND))
	if err != nil {
		_ = probe.Close()
		return nil, nil, err
	}
	return file, probe, nil
}

// All callers use nonblocking operations. Signals may interrupt them, but
// retries cannot override cancellation or introduce a blocking-open join.
func bashPPFIFORetry(ctx context.Context, op func() error) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := op(); err != unix.EINTR {
			return err
		}
	}
}
