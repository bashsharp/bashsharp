// Copyright (c) 2026, the outpost authors
// See LICENSE for licensing information

//go:build windows

package interp

import (
	"io/fs"
	"os"
	"time"
)

// Windows has no /dev/tty, but bash's fixtures (and scripts) assume one
// exists as a character device even when no terminal is attached (Linux
// keeps /dev/tty in the filesystem regardless; opening it without a
// controlling terminal is what fails). So on Windows:
//
//   - stat("/dev/tty") always reports a character device (`test -c`);
//   - open("/dev/tty") is the console: CONIN$ for reading, CONOUT$ for
//     writing — it fails, as on a Linux host without a controlling
//     terminal, when the process has no console.

const devTTY = "/dev/tty"

// devTTYInfo is the synthetic stat result for /dev/tty.
type devTTYInfo struct{}

func (devTTYInfo) Name() string       { return "tty" }
func (devTTYInfo) Size() int64        { return 0 }
func (devTTYInfo) Mode() fs.FileMode  { return fs.ModeDevice | fs.ModeCharDevice | 0o666 }
func (devTTYInfo) ModTime() time.Time { return time.Time{} }
func (devTTYInfo) IsDir() bool        { return false }
func (devTTYInfo) Sys() any           { return nil }

// devTTYStat reports the synthetic /dev/tty stat when path names it.
func devTTYStat(path string) (fs.FileInfo, bool) {
	if path == devTTY {
		return devTTYInfo{}, true
	}
	return nil, false
}

// devTTYConsolePath maps /dev/tty to the console device for the open mode.
func devTTYConsolePath(path string, flag int) (string, bool) {
	if path != devTTY {
		return "", false
	}
	if flag&(os.O_WRONLY|os.O_RDWR) != 0 {
		return `CONOUT$`, true
	}
	return `CONIN$`, true
}
