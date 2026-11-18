//go:build full && windows

package interp_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Windows syscall.Exec always returns EWINDOWS. The returned error must
// compare non-nil before the original source panics with that value.
func TestGoSourceWindowsExecErrorNilComparison(t *testing.T) {
	const source = `package main
import "syscall"
func main() { err := syscall.Exec("", nil, nil); if err != nil { panic(err) } }`
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
}
