// Copyright (c) 2026, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

//go:build windows

package interp

import "os"

// closedStdioFile stands for a closed standard descriptor in a child's
// startup info. A closed *os.File will not do here: its Fd() is
// INVALID_HANDLE_VALUE, which doubles as the current-process pseudo-handle,
// and syscall.StartProcess duplicates every non-zero std handle into the
// child (exec_windows.go) — the child would then see a process handle as
// fd 1 and report "cat: write error" of the wrong kind. Handle value 0 is
// the one value StartProcess skips, leaving the child a NULL std handle,
// which is exactly how a closed stdio slot is observed on Windows.
//
// The file is package-held so its finalizer never runs: closing handle 0
// is harmless but pointless, and a live *os.File is what os/exec expects.
var closedStdioFile = os.NewFile(0, "bashy-closed-stdio")

// closedExecFile returns the shared handle-0 placeholder; see
// closedStdioFile.
func closedExecFile() (*os.File, error) {
	return closedStdioFile, nil
}
