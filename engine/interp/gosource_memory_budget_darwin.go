package interp

import "golang.org/x/sys/unix"

func goSourceHostMemory() int64 {
	n, err := unix.SysctlUint64("hw.memsize")
	if err != nil || n >= 1<<63 {
		return 0
	}
	return int64(n)
}
