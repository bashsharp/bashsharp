// Copyright (c) 2026, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

//go:build !windows

package interp

import "os"

// closedExecFile returns an *os.File whose descriptor has already been
// closed. os/exec treats its invalid descriptor as a request to leave the
// corresponding child fd closed, rather than synthesizing /dev/null or a
// live copy pipe for a nil or generic Reader/Writer.
func closedExecFile() (*os.File, error) {
	f, err := os.Open(os.DevNull)
	if err != nil {
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	return f, nil
}
