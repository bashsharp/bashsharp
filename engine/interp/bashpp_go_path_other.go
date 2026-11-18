//go:build !windows

package interp

import (
	"os"
	"path/filepath"
)

func bashPPGoBinaryPath(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", &os.PathError{Op: "resolve Go binary", Path: path, Err: err}
	}
	return resolved, nil
}
