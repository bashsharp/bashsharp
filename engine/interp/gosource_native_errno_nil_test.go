//go:build full

package interp_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGoSourceNativeErrnoNilComparison(t *testing.T) {
	for name, source := range map[string]string{
		"non_nil": `package main
import ("fmt"; "os")
func main() { _, err := os.Stat("__missing_go_source_file__"); if err != nil { fmt.Println("non-nil", err) } }`,
		"nil": `package main
import ("fmt"; "os")
func main() { _, err := os.Stat("."); if err == nil { fmt.Println("nil") } }`,
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "original.go")
			if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
				t.Fatal(err)
			}
			want := runNativeOracle(t, dir, path, nil, "")
			got := runGoSourceRunner(t, dir, path, source, nil, "")
			panicHead := func(s string) string { return strings.SplitN(s, "\n\n", 2)[0] }
			if got.status != want.status || got.stdout != want.stdout || panicHead(got.stderr) != panicHead(want.stderr) {
				t.Fatalf("interpreted=%+v native=%+v", got, want)
			}
		})
	}
}
