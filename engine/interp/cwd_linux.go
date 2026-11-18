// Copyright (c) 2017, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

//go:build linux

package interp

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func currentWorkingDir() (string, error) {
	path, err := os.Getwd()
	if err == nil || !errors.Is(err, unix.ENAMETOOLONG) {
		return path, err
	}
	// Linux getcwd(2) rejects a result beyond PATH_MAX even though the
	// process still has a perfectly usable cwd inode. procfs readlink does
	// not impose that pathname limit, and os.Readlink grows its buffer until
	// the complete target is returned. This is the cold-start counterpart to
	// the retained dirFile used after the runner has changed directory.
	return os.Readlink("/proc/self/cwd")
}

func openRunnerDir(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENAMETOOLONG) {
		return openLongDir(path)
	}
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
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
	if r.dirFile == nil {
		return fallback
	}
	path := fmt.Sprintf("/proc/self/fd/%d", r.dirFile.Fd())
	if _, err := os.Stat(path); err != nil {
		return fallback
	}
	return path
}

func runnerPhysicalDir(r *Runner, fallback string) string {
	if r.dirFile == nil {
		return fallback
	}
	path := fmt.Sprintf("/proc/self/fd/%d", r.dirFile.Fd())
	if resolved, err := os.Readlink(path); err == nil {
		return resolved
	}
	return fallback
}
