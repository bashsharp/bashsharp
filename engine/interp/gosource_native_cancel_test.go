//go:build full

package interp_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This is a failure-containment check, not an exit-status parity claim. A
// separate known limitation is that cancellation of the parent's receive can
// return status 1 even though the child's real status 7 remains diagnostic.
func TestGoSourceNativeCancellationPreservesTaskFailure(t *testing.T) {
	source := `package main
import("fmt";"os")
func main(){done:=make(chan bool);fmt.Println("before");go func(){os.Exit(7)}();<-done;fmt.Println("after")}`
	dir := t.TempDir()
	path := filepath.Join(dir, "original.go")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	want := runNativeOracle(t, dir, path, nil, "")
	if want.status != 7 || want.stdout != "before\n" || want.stderr != "" {
		t.Fatalf("oracle: %+v", want)
	}
	got := runGoSourceRunner(t, dir, path, source, nil, "")
	if got.status == 0 || got.stdout != want.stdout || !strings.Contains(got.stderr, "task failed: exit status 7") || strings.Contains(got.stderr, "context canceled") {
		t.Fatalf("real task failure was lost or replaced: %+v", got)
	}
	if after, err := os.ReadFile(path); err != nil || string(after) != source {
		t.Fatal("original bytes changed")
	}
}
