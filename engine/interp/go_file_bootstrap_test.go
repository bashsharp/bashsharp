package interp

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/bashsharp/bashsharp/engine/polyglot"
)

// Exercise the standalone bootstrap used by bashy's bashy_core source route,
// from a caller directory outside a module and without an injected toolchain.
func TestRunCompiledGoFileStandalone(t *testing.T) {
	testRunCompiledGoFileStandalone(t)
}

func testRunCompiledGoFileStandalone(t *testing.T) {
	t.Helper()
	t.Setenv("BASHPP_GO", "")
	previous := polyglot.ToolResolver
	polyglot.ToolResolver = nil
	t.Cleanup(func() { polyglot.ToolResolver = previous })
	t.Chdir(t.TempDir())
	for _, tc := range []struct {
		name, source, output string
		status               int
	}{
		{"main.go", "package main\nfunc main(){println(\"standalone\")}\n", "standalone\n", 0},
		{"library.go", "package library\n", "", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(tc.name, []byte(tc.source), 0600); err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			status, err := RunCompiledGoFile(tc.name, nil, nil, &out, &out)
			if tc.status == 2 {
				want, absErr := filepath.Abs(tc.name)
				if absErr != nil {
					t.Fatal(absErr)
				}
				want += ":1:1: Go source package library cannot run as a program; expose its exported functions from a ~~~go fence in a .bsh script"
				if status != 2 || err == nil || err.Error() != want {
					t.Fatalf("status=%d err=%v; want %s", status, err, want)
				}
			} else if err != nil || status != tc.status || out.String() != tc.output {
				t.Fatalf("status=%d err=%v output=%q", status, err, out.String())
			}
		})
	}
}
