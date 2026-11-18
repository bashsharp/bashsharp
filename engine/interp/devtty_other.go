// Copyright (c) 2026, the outpost authors
// See LICENSE for licensing information

//go:build !windows

package interp

import (
	"io/fs"
)

// Off Windows the OS provides /dev/tty; nothing is synthesized.
func devTTYStat(string) (fs.FileInfo, bool)        { return nil, false }
func devTTYConsolePath(string, int) (string, bool) { return "", false }
