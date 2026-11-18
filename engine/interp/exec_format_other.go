//go:build !windows

package interp

import (
	"errors"
	"strings"
	"syscall"
)

func isExecFormatError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "exec format error")
}

// Kept on non-Windows hosts so ERROR_BAD_EXE_FORMAT wrapper handling is
// covered by the common test suite as well as the Windows build.
func isExecFormatErrorWindows(err error) bool { return errors.Is(err, syscall.Errno(193)) }
