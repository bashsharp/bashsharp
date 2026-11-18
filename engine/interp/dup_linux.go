//go:build linux

package interp

import "golang.org/x/sys/unix"

// dupFD copies oldfd onto newfd. On Linux the dup2 syscall is absent on
// arm64/riscv64/loong64, so use dup3 (present on every Linux arch). Callers
// guarantee oldfd != newfd, so dup3's EINVAL-on-equal case never triggers.
func dupFD(oldfd, newfd int) error { return unix.Dup3(oldfd, newfd, 0) }
