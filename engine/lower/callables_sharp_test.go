//go:build full

package lower_test

import (
	"github.com/bashsharp/bashsharp/engine/lower"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSharpNativeCalls(t *testing.T) {
	for _, src := range []string{
		`func greet(name string, retries int = 3) { printf '%s:%d\n' name retries }
greet("Ada")
greet(retries: 5, name: "Lin")
`,
		`fallback := 3
func show(a int = fallback, b int = 4) { printf '%s:%s\n' "$a" "$b" }
show(b: 2, a: 1)
func main() {
 fallback := 9
 defer show(b: 5)
 fallback = 8
}
main()
`,
	} {
		t.Run(src, func(t *testing.T) { execute(t, compile(t, src)) })
	}
}

// Exercise public callable/default/enum sources without rewriting fixtures.
// Readonly artifact execution belongs to its subsequent runtime slice.
func TestSharpCallPublicManifest(t *testing.T) {
	root := os.Getenv("BASHSHARP_CORPUS")
	if root == "" {
		t.Skip("set BASHSHARP_CORPUS to public tests/bashsharp")
	}
	manifests, err := filepath.Glob(filepath.Join(root, "*", "lowering.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	for _, manifest := range manifests {
		data, err := os.ReadFile(manifest)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(data), "\n") {
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			row := strings.Split(line, "\t")
			if len(row) != 6 {
				t.Fatalf("bad manifest row %q", line)
			}
			if group := filepath.Base(filepath.Dir(manifest)); group != "defaults" && group != "kwargs" && group != "enums" && group != "null-safety" {
				continue
			}
			path := filepath.Join(filepath.Dir(manifest), row[1])
			source, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Run(filepath.Base(filepath.Dir(manifest))+"/"+row[0], func(t *testing.T) {
				file := parse(t, string(source), path)
				result, err := lower.Compile(file, lower.Options{})
				if row[2] == "reject" {
					if err == nil || result != nil {
						t.Fatal("expected static rejection and no artifact")
					}
					expected, e := os.ReadFile(filepath.Join(filepath.Dir(path), row[5]))
					if e != nil {
						t.Fatal(e)
					}
					if !strings.Contains(err.Error(), strings.TrimSpace(string(expected))) {
						t.Fatalf("diagnostic %v, want %s", err, expected)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				execute(t, compiledCase{Result: result, input: string(source)})
			})
		}
	}
}
