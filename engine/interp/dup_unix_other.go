//go:build unix && !linux

package interp

import "golang.org/x/sys/unix"

// dupFD copies oldfd onto newfd on non-Linux unixes (darwin/BSD), which
// provide dup2 directly.
func dupFD(oldfd, newfd int) error { return unix.Dup2(oldfd, newfd) }
