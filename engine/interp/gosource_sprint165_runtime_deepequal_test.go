//go:build full

package interp_test

import (
	"os"
	"path/filepath"
	"testing"
)

// reflect.DeepEqual reads interpreter-owned operands and leaves them as they
// were.
func TestGoSourceSprint165ReflectDeepEqualReadOnly(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "sprint165", "runtime-1", "reflect-readonly", "deep_equal.go.txt"))
	if err != nil {
		t.Fatal(err)
	}
	differGoSource(t, string(data), nil, "")
}
