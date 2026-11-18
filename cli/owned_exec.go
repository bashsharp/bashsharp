package cli

import (
	"os"
	"path/filepath"
	"runtime"
)

// ownedExecutablePaths names installed Bashy child images. interp checks
// file identity against these paths after resolving the command, so a PATH
// entry merely named "yoke" cannot opt an unrelated program into the frame.
func ownedExecutablePaths() []string {
	self, err := os.Executable()
	if err != nil {
		return nil
	}
	paths := []string{self}
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	paths = append(paths, filepath.Join(filepath.Dir(self), "yoke"+suffix))
	if root := os.Getenv("BASHY_ROOT"); root != "" {
		paths = append(paths,
			filepath.Join(root, "usr", "bin", "yoke"+suffix),
			filepath.Join(root, "bin", "yoke"+suffix))
	}
	return paths
}
