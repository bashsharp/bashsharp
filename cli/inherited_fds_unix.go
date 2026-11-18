//go:build unix

package cli

import (
	"syscall"

	"golang.org/x/sys/unix"
)

func discoverInheritedFds() []int {
	var fds []int
	for fd := 3; fd < 256; fd++ {
		if _, err := unix.FcntlInt(uintptr(fd), syscall.F_GETFD, 0); err == nil {
			fds = append(fds, fd)
		}
	}
	return fds
}
