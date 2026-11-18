package lower_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/lower"
)

// TestGoSourcePragmaPosition drives an outside-corpus rejected pragma through
// the real compiler. The exact singleton diagnostic rejects missing, extra,
// duplicate, wrong-line, wrong-wording, and unexpected-success outcomes. A
// supported noinline pragma is the positive control.
func TestGoSourcePragmaPosition(t *testing.T) {
	dir := filepath.Join("testdata", "sprint165", "lower-1", "pragma-position")
	for _, tc := range []struct {
		name       string
		wantStatus bool
		wantDiag   string
	}{
		{name: "supported.go"},
		{name: "invalid.go", wantStatus: true, wantDiag: ":3:3: //go:nowritebarrier only allowed in runtime"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(dir, tc.name))
			if err != nil {
				t.Fatal(err)
			}
			program, err := gosource.Parse(bytes.NewReader(data), tc.name, gosource.Options{})
			if err != nil {
				t.Fatal(err)
			}
			result, err := lower.Compile(program.File, lower.Options{Origin: tc.name, Package: program.Package})
			if err != nil {
				t.Fatal(err)
			}
			generated := filepath.Join(t.TempDir(), "generated.go")
			if err := os.WriteFile(generated, result.Source, 0o600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("go", "tool", "compile", "-p=pragmafixture", "-o", generated+".o", generated)
			cmd.Env = append(os.Environ(), "GOTOOLCHAIN=go1.27.1")
			output, runErr := cmd.CombinedOutput()
			if (runErr != nil) != tc.wantStatus {
				t.Fatalf("compiler status error = %v, want failure %v\n%s\n--- generated\n%s", runErr, tc.wantStatus, output, result.Source)
			}
			if tc.wantDiag == "" {
				if len(output) != 0 {
					t.Fatalf("compiler emitted stray output %q", output)
				}
				return
			}
			lines := strings.Split(strings.TrimSpace(string(output)), "\n")
			want := filepath.Join(filepath.Dir(generated), tc.name) + tc.wantDiag
			if len(lines) != 1 || lines[0] != want {
				t.Fatalf("compiler diagnostics = %q, want exactly %q\n--- generated\n%s", lines, want, result.Source)
			}
		})
	}
}
