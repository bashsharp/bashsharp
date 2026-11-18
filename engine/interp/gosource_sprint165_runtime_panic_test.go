//go:build full

package interp_test

import (
	"os"
	"path/filepath"
	"testing"
)

// Sprint 165 runtime lane — runtime faults raised by their message alone
// recover as the runtime's error values.
func TestGoSourceSprint165RecoveredRuntimeErrors(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "sprint165", "runtime-1", "runtime-panic", "recovered_runtime_errors.go.txt"))
	if err != nil {
		t.Fatal(err)
	}
	differGoSource(t, string(data), nil, "")
}
