//go:build !windows

package interp

func preparePlatformExec(_ string, execPath, _ string, args []string) (string, []string, string, func()) {
	return execPath, args, "", nil
}
