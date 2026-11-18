// Copyright (c) 2026, the bashy authors
// See LICENSE for licensing information

//go:build unix

package interp_test

import (
	"fmt"
	"os"
)

// cgFDDir is the kernel's per-process open-fd directory. Linux exposes
// /proc/self/fd; the BSDs and Darwin expose /dev/fd. We try both so the leak
// detector works across the unix targets CI builds.
func cgFDDir() string {
	for _, dir := range []string{"/proc/self/fd", "/dev/fd"} {
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			return dir
		}
	}
	return ""
}

// cgCountFDs returns the number of open file descriptors for this process.
func cgCountFDs() (int, error) {
	dir := cgFDDir()
	if dir == "" {
		return 0, fmt.Errorf("no fd directory available on this unix")
	}
	f, err := os.Open(dir)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	names, err := f.Readdirnames(-1)
	if err != nil {
		return 0, err
	}
	// Reading the directory itself consumes one fd (f); discount it so a
	// clean run reports a zero delta.
	return len(names) - 1, nil
}

// cgListFDs returns a human-readable listing of the open fds and, where the
// kernel exposes it, what each points at — so a leak names the pipe/file, not
// just a number.
func cgListFDs() ([]string, error) {
	dir := cgFDDir()
	if dir == "" {
		return nil, fmt.Errorf("no fd directory available on this unix")
	}
	f, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	names, err := f.Readdirnames(-1)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(names))
	for _, n := range names {
		target, lerr := os.Readlink(dir + "/" + n)
		if lerr != nil {
			out = append(out, "fd "+n)
			continue
		}
		out = append(out, fmt.Sprintf("fd %s -> %s", n, target))
	}
	return out, nil
}
