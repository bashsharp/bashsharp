//go:build linux

package expand

import (
	"io/fs"
	"syscall"
	"time"
)

func fileAccessTime(info fs.FileInfo) (time.Time, bool) {
	data, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return time.Time{}, false
	}
	return time.Unix(int64(data.Atim.Sec), int64(data.Atim.Nsec)), true
}
