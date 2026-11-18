// Copyright (c) 2017, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

//go:build linux

package interp

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// openLongPath walks an absolute path one component at a time. Linux applies
// ENAMETOOLONG to a pathname passed in one syscall, even when every component
// is valid; openat keeps each syscall operand bounded. Intermediate components
// must be directories, while finalFlags controls the final component.
func openLongPath(path string, finalFlags int) (*os.File, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("long path is not absolute: %q", path)
	}
	fd, err := unix.Open("/", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	components := strings.FieldsFunc(path, func(r rune) bool {
		return r == filepath.Separator
	})
	if strings.HasSuffix(path, string(filepath.Separator)) {
		finalFlags |= unix.O_DIRECTORY
	}
	for index, component := range components {
		if component == "" || component == "." {
			continue
		}
		flags := unix.O_PATH | unix.O_CLOEXEC
		if index < len(components)-1 {
			flags |= unix.O_DIRECTORY
		} else {
			flags |= finalFlags
		}
		next, openErr := unix.Openat(fd, component, flags, 0)
		_ = unix.Close(fd)
		if openErr != nil {
			return nil, openErr
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), path), nil
}

func openLongDir(path string) (*os.File, error) {
	return openLongPath(path, unix.O_DIRECTORY)
}

func statLongPath(path string, original error) (fs.FileInfo, error) {
	if !errors.Is(original, unix.ENAMETOOLONG) {
		return nil, original
	}
	file, err := openLongPath(path, 0)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return file.Stat()
}

func accessLongFile(path string, mode uint32, original error) error {
	if !errors.Is(original, unix.ENAMETOOLONG) {
		return original
	}
	file, err := openLongPath(path, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	return unix.Faccessat(int(file.Fd()), "", mode, unix.AT_EACCESS|unix.AT_EMPTY_PATH)
}

func openLongExecPath(path string) (*os.File, bool, error) {
	_, err := os.Stat(path)
	if !errors.Is(err, unix.ENAMETOOLONG) {
		return nil, false, nil
	}
	file, err := openLongPath(path, 0)
	return file, true, err
}

func accessLongPath(path string, mode uint32, original error) error {
	return accessLongFile(path, mode, original)
}

func physicalLongPath(path string, original error) (string, error) {
	if !errors.Is(original, unix.ENAMETOOLONG) {
		return "", original
	}
	fd, err := unix.Open("/", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return "", err
	}
	defer unix.Close(fd)

	pending := strings.Split(path, string(filepath.Separator))
	resolved := make([]string, 0, len(pending))
	symlinks := 0
	for len(pending) > 0 {
		component := pending[0]
		pending = pending[1:]
		switch component {
		case "", ".":
			continue
		case "..":
			next, openErr := unix.Openat(fd, "..", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
			if openErr != nil {
				return "", openErr
			}
			_ = unix.Close(fd)
			fd = next
			if len(resolved) > 0 {
				resolved = resolved[:len(resolved)-1]
			}
			continue
		}

		target, linkErr := readlinkAt(fd, component)
		if linkErr == nil {
			symlinks++
			if symlinks > 40 {
				return "", unix.ELOOP
			}
			targetParts := strings.Split(target, string(filepath.Separator))
			if filepath.IsAbs(target) {
				next, openErr := unix.Open("/", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
				if openErr != nil {
					return "", openErr
				}
				_ = unix.Close(fd)
				fd = next
				resolved = resolved[:0]
			}
			pending = append(targetParts, pending...)
			continue
		}
		if !errors.Is(linkErr, unix.EINVAL) {
			return "", linkErr
		}

		next, openErr := unix.Openat(fd, component, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		if openErr != nil {
			return "", openErr
		}
		_ = unix.Close(fd)
		fd = next
		resolved = append(resolved, component)
	}
	return string(filepath.Separator) + strings.Join(resolved, string(filepath.Separator)), nil
}

func readlinkAt(dirfd int, name string) (string, error) {
	buffer := make([]byte, 256)
	for {
		n, err := unix.Readlinkat(dirfd, name, buffer)
		if err != nil {
			return "", err
		}
		if n < len(buffer) {
			return string(buffer[:n]), nil
		}
		buffer = make([]byte, len(buffer)*2)
	}
}
