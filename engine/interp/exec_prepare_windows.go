//go:build windows

package interp

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// preparePlatformExec settles what to hand os/exec for execPath on
// Windows: a shebang script runs through its interpreter (this binary
// when that is bash or sh), and an extensionless PE image — which os/exec
// would refuse — through a name it accepts (exec_pe.go). The returned
// cleanup, when non-nil, is to run after the command has been waited for;
// the third result names a shebang interpreter that does not exist.
func preparePlatformExec(dir, execPath, diagnosticPath string, args []string) (string, []string, string, func()) {
	data, err := readShebangProbe(diagnosticPath)
	if err != nil {
		return execPath, args, "", nil
	}
	interp, optarg, ok := parseShebang(data)
	if !ok {
		self, _ := os.Executable()
		if path, cleanup, ok := planExtensionlessPEExec(execPath, data, self, os.TempDir()); ok {
			return path, args, "", cleanup
		}
		// Go's os/exec refuses an extensionless plain script during its
		// Windows suffix lookup, before CreateProcess can report ENOEXEC.
		// Run it through this shell just as the Unix exec-format fallback
		// does. A prior LookPathDir check admitted this name under the
		// Windows executable rule; this second stat prevents a vanished file
		// from turning a genuine not-found into a shell invocation.
		if !winHasExt(execPath) {
			if info, statErr := os.Stat(execPath); statErr == nil && info.Mode().IsRegular() && self != "" {
				return self, shebangArgs(self, "", execPath, args), "", nil
			}
		}
		return execPath, args, "", nil
	}
	if strings.ContainsAny(interp, "\x00") {
		return execPath, args, "", nil
	}
	resolved := interp
	if shellPathAbs(interp) {
		resolved = shellPathToOS(dir, interp)
	} else if found, err := exec.LookPath(interp); err == nil {
		resolved = found
	}
	if _, err := os.Stat(resolved); err != nil {
		if _, extErr := os.Stat(resolved + ".exe"); extErr == nil {
			resolved += ".exe"
		} else if base := strings.TrimSuffix(strings.ToLower(filepath.Base(interp)), ".exe"); base == "bash" || base == "sh" {
			if self, selfErr := os.Executable(); selfErr == nil {
				resolved = self
			} else {
				return execPath, args, interp, nil
			}
		} else {
			return execPath, args, interp, nil
		}
	}
	if self, err := os.Executable(); err == nil {
		if a, aerr := os.Stat(resolved); aerr == nil {
			if b, berr := os.Stat(self); berr == nil && os.SameFile(a, b) {
				resolved = self
			}
		}
	}
	return resolved, shebangArgs(resolved, optarg, execPath, args), "", nil
}
