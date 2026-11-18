//go:build !unix

package cli

// maybeNewProcessGroup is a no-op on platforms without POSIX process groups
// (e.g. Windows). See pgrp_unix.go for the Unix implementation.
func maybeNewProcessGroup() {}
