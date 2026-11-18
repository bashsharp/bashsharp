//go:build full

package interp_test

import (
	"os"
	"path/filepath"
	"testing"
)

// A dependency-owned dynamic value asserted to an interface answers from the
// dependency, and its methods bind there.
func TestGoSourceSprint165NativeInterfaceAssertions(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "sprint165", "runtime-1", "native-assert", "dependency_values.go.txt"))
	if err != nil {
		t.Fatal(err)
	}
	differGoSource(t, string(data), nil, "")
}
