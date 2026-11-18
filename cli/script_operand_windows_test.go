//go:build windows

package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// GNU Bash's invocation fixture runs `${THIS_SH} ls` after setting
// PATH=/bin:/usr/bin. The child shell must find ls through its own POSIX
// mount path, then open that binary as the script operand.
func TestWindowsPOSIXPathScriptOperand(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the pure Bash executable")
	}
	dir := t.TempDir()
	root := filepath.Join(dir, "root")
	bin := filepath.Join(root, "usr", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	bash := filepath.Join(bin, "bash.exe")
	build := exec.Command("go", "build", "-buildvcs=false", "-o", bash, "./cmd/bash")
	build.Dir = filepath.Join("..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build bash: %v\n%s", err, out)
	}
	if err := os.Link(bash, filepath.Join(bin, "bash")); err != nil {
		t.Fatal(err)
	}
	// The extensionless twin mirrors the fixture userland's Cygwin view of
	// an executable image. The NUL in its first line is the binary probe.
	if err := os.WriteFile(filepath.Join(bin, "ls"), []byte{'M', 'Z', 0, 1}, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bash, "-c", "PATH=/bin:/usr/bin; /usr/bin/bash ls")
	cmd.Env = append(os.Environ(), "BASHY_ROOT="+root)
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "cannot execute binary file") {
		t.Fatalf("bash ls: error=%v, output=%q", err, out)
	}
}
