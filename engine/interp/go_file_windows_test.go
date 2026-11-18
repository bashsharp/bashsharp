package interp

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestRunCompiledGoFileJunctionGOROOT(t *testing.T) {
	// An SDK can be exposed through a directory junction. Junctions
	// require neither symlink privileges nor developer mode on Windows.
	root := runtime.GOROOT()
	junction := filepath.Join(t.TempDir(), "sdk-junction")
	cmd := exec.Command("cmd", "/c", "mklink", "/J", junction, root)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create SDK junction: %v: %s", err, out)
	}
	t.Cleanup(func() {
		if err := os.Remove(junction); err != nil {
			t.Errorf("remove SDK junction: %v", err)
		}
	})
	t.Setenv("GOROOT", junction)
	previous := bashPPRuntimeGOROOT
	bashPPRuntimeGOROOT = func() string { return junction }
	t.Cleanup(func() { bashPPRuntimeGOROOT = previous })
	t.Run("standalone", testRunCompiledGoFileStandalone)
	t.Run("injected", func(t *testing.T) {
		identity, err := bashPPInjectedGoIdentity(filepath.Join(junction, "bin", "go.exe"))
		if err != nil {
			t.Fatal(err)
		}
		want, err := os.Stat(filepath.Join(root, "bin", "go.exe"))
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.Stat(identity.Binary)
		if err != nil {
			t.Fatal(err)
		}
		if !os.SameFile(got, want) {
			t.Fatalf("resolved binary %q does not identify SDK binary in %q", identity.Binary, root)
		}
	})
}
