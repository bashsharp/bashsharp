// Copyright (c) 2026, the bashy authors
// See LICENSE for licensing information

//go:build !linux

package interp

func watchExecReplacementStops(pid int) func() { return func() {} }
