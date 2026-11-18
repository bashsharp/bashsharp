//go:build full

package interp_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A native error can arrive through a local error interface. Its panic text
// must use Error(), while recover must still see the original concrete type.
func TestGoSourceNativeErrorPanicText(t *testing.T) {
	for name, source := range map[string]string{
		"plain": `package main
func main() { panic("plain") }`,
		"direct_native_error": `package main
import "fmt"
func main() { panic(fmt.Errorf("direct native error")) }`,
		"unrecovered": `package main
import "os/exec"
func main() { _, err := exec.LookPath("__bashpp_missing_executable__"); panic(err) }`,
		"recovered": `package main
import ("fmt"; "os/exec")
func main() {
	 defer func() {
	  e, ok := recover().(*exec.Error)
	  fmt.Println(ok, e.Name, e.Error())
	 }()
	 _, err := exec.LookPath("__bashpp_missing_executable__")
	 panic(err)
}`,
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
