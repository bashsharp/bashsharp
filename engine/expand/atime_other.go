//go:build !windows && !linux && !darwin

package expand

import (
	"io/fs"
	"time"
)

func fileAccessTime(info fs.FileInfo) (time.Time, bool) {
	return time.Time{}, false
}
