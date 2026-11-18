//go:build windows

package expand

import (
	"io/fs"
	"syscall"
	"time"
)

func fileAccessTime(info fs.FileInfo) (time.Time, bool) {
	data, ok := info.Sys().(*syscall.Win32FileAttributeData)
	if !ok {
		return time.Time{}, false
	}
	return time.Unix(0, data.LastAccessTime.Nanoseconds()), true
}
