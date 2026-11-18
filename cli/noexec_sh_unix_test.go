//go:build unix

package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestNoExecParsesWithoutRunning pins `-n` (noexec) for every name bashy
// answers to: `sh -n script` and `bashy -n script` parse the script and
// never run it (story 51e876cd: reported as `sh -n` executing check.sh).
func TestNoExecParsesWithoutRunning(t *testing.T) {
	binary := builtBashyBin(t)
	dir := t.TempDir()
	sh := filepath.Join(dir, "sh")
	if err := os.Symlink(binary, sh); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "ran")
	script := filepath.Join(dir, "check.sh")
	body := "set -u\n: > '" + marker + "'\necho RAN $undefined_var\n"
	if err := os.WriteFile(script, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{sh, "-n", script},
		{binary, "-n", script},
		{binary, "--posix", "-n", script},
		{sh, "-n", "--", script},
		{binary, "-o", "noexec", script},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Env = []string{"HOME=" + dir, "PATH=/bin:/usr/bin", "LC_ALL=C"}
		out, err := cmd.CombinedOutput()
		if err != nil || len(out) != 0 {
			t.Errorf("%q: err=%v output=%q, want a silent successful parse", args[1:], err, out)
		}
		if _, statErr := os.Stat(marker); statErr == nil {
			t.Fatalf("%q ran the script", args[1:])
		}
	}
}
