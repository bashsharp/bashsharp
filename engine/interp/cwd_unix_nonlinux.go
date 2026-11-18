// Copyright (c) 2017, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

//go:build unix && !linux

package interp

import (
	"os"

	"golang.org/x/sys/unix"
)

func currentWorkingDir() (string, error) { return os.Getwd() }

func openRunnerDir(path string) (*os.File, error) {
	return os.Open(path)
}

func openRunnerDirAt(file *os.File, path string) (*os.File, bool, error) {
	if file == nil {
		return nil, false, nil
	}
	fd, err := unix.Openat(int(file.Fd()), path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, true, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(fd), path), true, nil
}

func dupRunnerDir(file *os.File) (*os.File, error) {
	if file == nil {
		return nil, nil
	}
	fd, err := unix.FcntlInt(file.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), file.Name()), nil
}

func runnerExecDir(r *Runner, fallback string) string {
	return fallback
}
