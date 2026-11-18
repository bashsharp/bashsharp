//go:build unix

package interp

import (
	"errors"
	"syscall"
)

func isPlatformBrokenPipeWriteErr(err error) bool {
	return errors.Is(err, syscall.EPIPE)
}
