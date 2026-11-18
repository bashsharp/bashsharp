// Copyright (c) 2026, the outpost authors
// See LICENSE for licensing information

//go:build linux

package interp

func standaloneRuntimeDefaultSignalNames() []string {
	return []string{"BUS", "FPE", "ILL", "SEGV", "TRAP"}
}
