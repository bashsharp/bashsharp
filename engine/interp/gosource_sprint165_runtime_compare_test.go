//go:build full

package interp_test

import (
	"os"
	"path/filepath"
	"testing"
)

// An untyped constant compared with a dependency call's result takes the
// result's type before the dependency compares them.
func TestGoSourceSprint165NativeCompareUntypedOperands(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "sprint165", "runtime-1", "native-compare", "untyped_operands.go.txt"))
	if err != nil {
		t.Fatal(err)
	}
	differGoSource(t, string(data), nil, "")
}
