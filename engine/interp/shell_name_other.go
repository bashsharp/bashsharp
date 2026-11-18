//go:build !windows

package interp

func diagnosticShellName(name string) string { return diagnosticShellNameMode(name, false) }
