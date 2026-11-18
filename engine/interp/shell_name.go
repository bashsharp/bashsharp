package interp

import "strings"

func diagnosticShellNameMode(name string, windows bool) string {
	if windows && strings.HasSuffix(strings.ToLower(name), ".exe") {
		return name[:len(name)-4]
	}
	return name
}
