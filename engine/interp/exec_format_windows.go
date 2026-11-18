//go:build windows

package interp

import (
	"errors"
	"syscall"
)

// isExecFormatError reports ERROR_BAD_EXE_FORMAT as Windows' ENOEXEC.
// errors.Is follows *os.PathError and *exec.Error wrappers.
func isExecFormatError(err error) bool {
	return isExecFormatErrorWindows(err)
}

func isExecFormatErrorWindows(err error) bool { return errors.Is(err, syscall.Errno(193)) }
