package lower_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/lower"
)

// A standalone source may use the embedded runtime declarations without
// requiring a copy of the engine module in its own project. Runtime adapters
// must not make an unrelated typed shell command resolve foreign-worker code.
func TestRuntimeDeclarationsOutsideEngineModule(t *testing.T) {
	t.Setenv("GOWORK", "off")
	t.Setenv("GO111MODULE", "on")
	t.Setenv("GOPROXY", "off")
	t.Setenv("GOSUMDB", "off")
	for _, project := range []bool{false, true} {
		name := "standalone"
		if project {
			name = "selected-project"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			source := "var x int = 10\necho hi\n"
			if project {
				if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/selected\n\ngo 1.25\n"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(filepath.Join(root, "dep"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, "dep", "value.go"), []byte("package dep\nfunc Value() int { return 73 }\n"), 0600); err != nil {
					t.Fatal(err)
				}
				source = "import \"example.test/selected/dep\"\nx := dep.Value()\necho \"$x\"\n"
			}
			origin := filepath.Join(root, "outside engine.bpp")
			result, err := lower.Compile(parse(t, source, origin), lower.Options{Dir: root, Origin: origin})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(result.Source), "github.com/bashsharp/bashsharp/engine/lower/shellrt") {
				t.Fatalf("fixture did not exercise runtime declarations:\n%s", result.Source)
			}
			if project && !strings.Contains(string(result.Source), "example.test/selected/dep") {
				t.Fatalf("selected project import lost:\n%s", result.Source)
			}
			if _, err := os.Stat(filepath.Join(root, "go.sum")); !os.IsNotExist(err) {
				t.Fatalf("standalone checking unexpectedly resolved module dependencies: %v", err)
			}
		})
	}
}
