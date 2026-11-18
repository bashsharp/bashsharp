//go:build darwin || linux

// Copyright (c) 2026, the outpost authors
// See LICENSE for licensing information

package interp

func mergeStartupIgnored(dst map[string]bool, name string) map[string]bool {
	if dst == nil {
		dst = make(map[string]bool)
	}
	dst[name] = true
	return dst
}
