//go:build full

package interp_test

import (
	"os"
	"path/filepath"
	"testing"
)

// A var declaration whose initializer spells a function value by name binds
// the closure the short declaration binds.
func TestGoSourceSprint165CallableDeclarationValues(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "sprint165", "runtime-1", "callable-decl", "function_values_by_name.go.txt"))
	if err != nil {
		t.Fatal(err)
	}
	differGoSource(t, string(data), nil, "")
}
