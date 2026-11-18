package interp

import (
	"os"
	"strings"

	"golang.org/x/sys/windows"
)

// Resolve through a file handle: filepath.EvalSymlinks can fail with a bare
// ERROR_PATH_NOT_FOUND for a Go SDK reached through a directory junction.
// Keep the resolved identity rather than falling back to an unresolved path.
func bashPPGoBinaryPath(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", &os.PathError{Op: "resolve Go binary", Path: path, Err: err}
	}
	defer file.Close()
	buf := make([]uint16, 256)
	for {
		n, err := windows.GetFinalPathNameByHandle(windows.Handle(file.Fd()), &buf[0], uint32(len(buf)), 0)
		if err != nil {
			return "", &os.PathError{Op: "resolve Go binary", Path: path, Err: err}
		}
		if n >= uint32(len(buf)) {
			buf = make([]uint16, n+1)
			continue
		}
		resolved := windows.UTF16ToString(buf[:n])
		// Convert extended DOS/UNC names back to ordinary absolute paths.
		// Leave other device namespaces intact.
		if strings.HasPrefix(resolved, `\\?\UNC\`) {
			resolved = `\\` + resolved[len(`\\?\UNC\`):]
		} else if strings.HasPrefix(resolved, `\\?\`) && len(resolved) >= 6 && resolved[5] == ':' {
			resolved = resolved[4:]
		}
		return resolved, nil
	}
}
