// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

//go:build !windows

package interp

import "io/fs"

// fifoMarkerStat is the identity off Windows: every other host has real
// FIFOs, which stat as FIFOs without any help from us. See fifo_marker.go
// for why the marker exists at all.
func fifoMarkerStat(path string, info fs.FileInfo) fs.FileInfo { return info }
