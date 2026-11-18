package interp

import (
	"os"
	"path/filepath"
	"runtime"
)

const execFD1ClosedEnv = "BASHY_EXEC_FD1_CLOSED"

// isInstalledCoreutilsExecutable checks the executable image, not the name
// used to invoke it. Installed Bashy shells keep their Go Coreutils image
// beside the shell payload; applet names may be symlinks to that image.
// Fail closed when the sibling image is absent or the path cannot be verified.
func isInstalledCoreutilsExecutable(path string) bool {
	if runtime.GOOS == "windows" {
		return false
	}
	self, err := os.Executable()
	if err != nil {
		return false
	}
	installed, err := os.Stat(filepath.Join(filepath.Dir(self), "coreutils"))
	if err != nil || !installed.Mode().IsRegular() {
		return false
	}
	candidate, err := os.Stat(path)
	return err == nil && candidate.Mode().IsRegular() && os.SameFile(candidate, installed)
}
