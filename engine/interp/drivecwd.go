// Copyright (c) 2026, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package interp

import "github.com/qiangli/bash/pathconv"

// Windows keeps a current directory per drive: `cd D:` returns to wherever
// the shell last was on D:, not to a fixed location. The runner models that
// with driveCwd, updated on every successful cd and consulted when a cd
// operand is a bare drive reference. The helpers are platform-neutral so the
// behavior is testable anywhere; the wiring in changeDir is Windows-only.

// driveOperandTarget resolves a bare drive operand ("D:", "d:") to that
// drive's recorded current directory, defaulting to the drive root. Any
// other operand reports ok=false.
func driveOperandTarget(operand string, driveCwd map[byte]string) (string, bool) {
	if len(operand) != 2 || operand[1] != ':' {
		return "", false
	}
	drive, ok := pathconv.DriveOf(operand)
	if !ok {
		return "", false
	}
	if cwd, ok := driveCwd[drive]; ok {
		return cwd, true
	}
	return string(drive) + `:\`, true
}

// recordDriveCwd notes dir as the current directory of its drive, allocating
// the map on first use. A dir without a drive prefix is ignored.
func recordDriveCwd(driveCwd map[byte]string, dir string) map[byte]string {
	drive, ok := pathconv.DriveOf(dir)
	if !ok {
		return driveCwd
	}
	if driveCwd == nil {
		driveCwd = make(map[byte]string, 1)
	}
	driveCwd[drive] = dir
	return driveCwd
}
