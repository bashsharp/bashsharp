//go:build full

package interp_test

// Sprint: #165; Story: #99; Story-ID: ca559d7ee23d
//
// A pointer to a dependency-owned struct value reads its fields through the
// dependency.

import (
	"os"
	"path/filepath"
	"testing"
)

func mustReadSprint165NativePointee(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "sprint165", "runtime-2", "native-pointee", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// chan/select2's shape: new(runtime.MemStats) filled by ReadMemStats and
// read field by field; `var m runtime.MemStats; &m` is the positive control
// in the same fixture.
func TestGoSourceSprint165NativePointeeFields(t *testing.T) {
	differGoSource(t, mustReadSprint165NativePointee(t, "memstats.go.txt"), nil, "")
}

// Negative: a nil pointer to an imported struct type still faults as Go's
// nil dereference when a field is read through it.
func TestGoSourceSprint165NativePointeeNil(t *testing.T) {
	differGoSource(t, mustReadSprint165NativePointee(t, "nil_pointee.go.txt"), nil, "")
}
