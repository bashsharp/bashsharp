// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

//go:build !unix

package interp

import (
	"os"
)

func bashPPTaskProbeOpen(dirFile *os.File, dir, path string, flags int, mode os.FileMode) (*os.File, bool, error) {
	return nil, false, nil
}

func bashPPTaskSourceClearNonblock(file *os.File) error { return nil }
