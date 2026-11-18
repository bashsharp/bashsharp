// Copyright (c) 2026, the outpost authors
// See LICENSE for licensing information

//go:build darwin

package interp

import (
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// writePipelineOutput keeps a pipe write's SIGPIPE on the simulated pipeline
// child that performed the write. Darwin's F_SETNOSIGPIPE turns the kernel
// signal into EPIPE at this descriptor boundary; pipelineWriter then assigns
// status 141 to the simulated child. A thread signal mask is insufficient on
// Darwin because a process-directed SIGPIPE may be delivered to another Go
// runtime thread and observed as a signal for the parent shell.
func writePipelineOutput(w io.Writer, p []byte) (int, error) {
	f, ok := w.(*os.File)
	if !ok {
		return w.Write(p)
	}
	old, err := unix.FcntlInt(f.Fd(), unix.F_GETNOSIGPIPE, 0)
	if err != nil {
		return f.Write(p)
	}
	if _, err := unix.FcntlInt(f.Fd(), unix.F_SETNOSIGPIPE, 1); err != nil {
		return f.Write(p)
	}
	defer unix.FcntlInt(f.Fd(), unix.F_SETNOSIGPIPE, old)
	return f.Write(p)
}
