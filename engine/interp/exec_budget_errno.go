// Copyright (c) 2026, the bashy authors
// See LICENSE for licensing information

//go:build unix || windows

package interp

import "syscall"

// execBudgetErr gives Bashy's policy refusal the same errno as a host execve
// argument-size refusal. Windows' syscall package defines the POSIX-compatible
// E2BIG sentinel even though CreateProcess uses different native errors.
func execBudgetErr() error { return syscall.E2BIG }
