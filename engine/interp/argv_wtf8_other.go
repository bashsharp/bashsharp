// Copyright (c) 2026, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

//go:build !windows

package interp

// execArgsForOS is the identity off Windows: execve takes argv as bytes.
func execArgsForOS(args []string) []string { return args }
