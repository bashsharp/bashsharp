// Copyright (c) 2026, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

//go:build windows

package interp

import (
	"os"

	"golang.org/x/sys/windows"
)

// dupPipeFd duplicates a pipe endpoint so each pipeline goroutine owns its
// endpoint outright and the parent can close the original, mirroring the
// Unix dup(2) path: EOF propagates when the writer closes its end, and a
// write after the reader is gone fails with a broken-pipe error instead of
// touching a shared, already-closed *os.File (mvdan/sh#1142). If duplication
// fails the original is shared, keeping the old best-effort behavior.
func dupPipeFd(f *os.File) (*os.File, bool, error) {
	proc := windows.CurrentProcess()
	var dup windows.Handle
	err := windows.DuplicateHandle(proc, windows.Handle(f.Fd()), proc, &dup,
		0, false, windows.DUPLICATE_SAME_ACCESS)
	if err != nil {
		return f, false, nil
	}
	return os.NewFile(uintptr(dup), f.Name()), true, nil
}
