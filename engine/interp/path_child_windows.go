//go:build windows

package interp

import (
	"os"
	"sync"

	"github.com/qiangli/bash/pathconv"
)

var selfExecContent struct {
	once sync.Once
	path string
	sum  [32]byte
	err  error
}

// isBashyChildImage accepts hard links and byte-identical copies of this
// executable. The fixture userland (and installed deployments) may stage sh
// on another Windows volume, where os.SameFile cannot recognise the copy.
func isBashyChildImage(path string) bool {
	if isSelfExecutable(path) {
		return true
	}
	selfExecContent.once.Do(func() {
		selfExecContent.path, selfExecContent.err = os.Executable()
		if selfExecContent.err == nil {
			selfExecContent.sum, selfExecContent.err = executableContentHash(selfExecContent.path)
		}
	})
	if selfExecContent.err != nil || !sameExecutableImage(path, selfExecContent.path) {
		return false
	}
	sum, err := executableContentHash(path)
	return err == nil && sum == selfExecContent.sum
}

func nativeExecEnvForChild(env []string, execPath string) []string {
	return nativeExecEnvForChildMountsMode(pathconv.CurrentMounts(), env, true, isBashyChildImage(execPath))
}
