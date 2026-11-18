package interp

import (
	"golang.org/x/sys/unix"
	"os"
)

func goSourceHostMemory() int64 {
	var info unix.Sysinfo_t
	var total int64
	if unix.Sysinfo(&info) == nil {
		n := uint64(info.Totalram) * uint64(info.Unit)
		if n > 0 && n < 1<<63 {
			total = int64(n)
		}
	}
	return goSourceCgroupMemory(total, os.ReadFile)
}
