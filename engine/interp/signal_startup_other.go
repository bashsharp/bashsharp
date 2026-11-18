//go:build !darwin && !linux

// Copyright (c) 2026, the outpost authors
// See LICENSE for licensing information

package interp

func startupIgnoredSignals(env string) map[string]bool {
	return parseHardIgnore(env)
}
