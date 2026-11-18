// Copyright (c) 2026, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

//go:build windows

package interp

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// isPlatformBrokenPipeWriteErr classifies the errors a Windows pipe write
// produces once the reader is gone. ERROR_BROKEN_PIPE and ERROR_NO_DATA
// ("the pipe is being closed") are the kernel's EPIPE equivalents; a write
// into an in-process pipe endpoint that a faster pipeline stage already
// closed surfaces as os.ErrClosed and must behave like EPIPE (status 141),
// not like a shell error.
func isPlatformBrokenPipeWriteErr(err error) bool {
	return errors.Is(err, windows.ERROR_BROKEN_PIPE) ||
		errors.Is(err, windows.ERROR_NO_DATA) ||
		errors.Is(err, os.ErrClosed)
}
