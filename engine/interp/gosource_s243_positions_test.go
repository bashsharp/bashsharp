//go:build full

package interp_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestS243RuntimePanicPositions runs the two unchanged Go 1.27.1 roots in
// native, interpreted, and lowered modes. Both programs self-check runtime
// frame lines: the selector identifier for a nil interface method call, and
// the '[' instruction (including its /*line*/ position) for bounds checks.
func TestS243RuntimePanicPositions(t *testing.T) {
	if runtime.Version() != "go1.27.1" {
		t.Skipf("requires unchanged Go 1.27.1 roots; have %s", runtime.Version())
	}
	for _, name := range []string{
		"devirtualization_nil_panics.go",
		filepath.Join("fixedbugs", "issue29504.go"),
	} {
		name := name
		t.Run(filepath.ToSlash(name), func(t *testing.T) {
			source, err := os.ReadFile(filepath.Join(runtime.GOROOT(), "test", name))
			if err != nil {
				t.Fatal(err)
			}
			typedSendThreeModes(t, string(source))
		})
	}
}
