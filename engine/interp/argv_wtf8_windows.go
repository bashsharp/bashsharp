// Copyright (c) 2026, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

//go:build windows

package interp

// execArgsForOS spells a child's argv for the UTF-16 command line: invalid
// bytes become Cygwin/MSYS lone surrogates. See argv_wtf8.go.
func execArgsForOS(args []string) []string { return EncodeWindowsArgs(args) }
