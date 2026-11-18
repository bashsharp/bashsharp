//go:build full

package lower_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/lower"
	"github.com/bashsharp/bashsharp/engine/polyglot"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// The fixture owner supplies authenticated official sources, manifests and a
// separately hashed observer. This test never manufactures or rewrites them.
// Select this test explicitly only after staging both fixture directories.
func TestCSharpOfficialGraphsBothModes(t *testing.T) {
	root := os.Getenv("BASHPP_CSHARP_OFFICIAL_GRAPHS")
	if root == "" {
		t.Skip("set BASHPP_CSHARP_OFFICIAL_GRAPHS to authenticated fixture root")
	}
	requirePowerShell(t)
	for _, name := range []string{"protected", "cs1705"} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(root, name)
			fileName := filepath.Join(dir, "observer.bsh")
			data, err := os.ReadFile(fileName)
			if err != nil {
				t.Fatal(err)
			}
			file := parse(t, string(data), fileName)
			if name == "cs1705" {
				// The expected location comes from the independently pinned native run.
				var expected struct {
					Stage       string                           `json:"stage"`
					Diagnostics []polyglot.CSharpGraphDiagnostic `json:"diagnostics"`
				}
				b, err := os.ReadFile(filepath.Join(dir, "expected-diagnostic.json"))
				if err != nil {
					t.Fatal(err)
				}
				if err = json.Unmarshal(b, &expected); err != nil {
					t.Fatal(err)
				}
				if expected.Stage != "root" || len(expected.Diagnostics) != 2 {
					t.Fatal("invalid native diagnostic stage/count")
				}
				for _, d := range expected.Diagnostics {
					if d.ID != "CS1705" || d.Location != "none" || d.Path != "" || d.Line != 1 || d.Column != 1 || d.Message == "" {
						t.Fatal("invalid native locationless diagnostic expectation")
					}
				}
				if expected.Diagnostics[0] != expected.Diagnostics[1] {
					t.Fatal("native expectation must retain both identical errors")
				}
				// Validate the structured compiler stage separately from product wrapping.
				env, err := polyglot.DiscoverEnvironment(polyglot.EnvironmentRequest{Source: fileName, Language: "csharp"})
				if err != nil {
					t.Fatal(err)
				}
				c := polyglot.CSharp{Environment: &env, CacheDir: t.TempDir()}
				_, err = c.Analyze(context.Background(), "public static int Observer() => 0;")
				var failure *polyglot.CSharpGraphError
				if !errors.As(err, &failure) {
					t.Fatalf("expected structured compilation failure, got %v", err)
				}
				// Preserve native multiplicity, location and complete identity message.
				if failure.Stage != expected.Stage || len(failure.Diagnostics) != len(expected.Diagnostics) {
					t.Fatalf("wrong stage/diagnostic count: %#v", failure)
				}
				for i, d := range failure.Diagnostics {
					want := expected.Diagnostics[i]
					want.Severity = "error"
					if d != want {
						t.Fatalf("diagnostic %d: got %#v, want %#v", i, d, want)
					}
				}
				_, err = lower.Compile(file, lower.Options{})
				if err == nil || !strings.Contains(err.Error(), "error CS1705:") || !strings.Contains(err.Error(), "[assembly root]") {
					t.Fatalf("compiled mode: %v", err)
				}
				var out, stderr bytes.Buffer
				runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(dir), interp.StdIO(nil, &out, &stderr))
				if err != nil {
					t.Fatal(err)
				}
				err = runner.Run(context.Background(), file)
				if err == nil || !strings.Contains(err.Error(), "error CS1705:") || !strings.Contains(err.Error(), "[assembly root]") {
					t.Fatalf("interpreted mode: %v %s", err, &stderr)
				}
				return
			}
			if got := testPythonFenceInterpretedNativeParityAt(t, string(data), fileName); got != "2\n" {
				t.Fatalf("protected official observer output %q", got)
			}
		})
	}
}
