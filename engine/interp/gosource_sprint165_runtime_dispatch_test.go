//go:build full

package interp_test

import (
	"os"
	"path/filepath"
	"testing"
)

// A method call resolves by the receiver's type: a local type's original
// body runs even when the value is held natively, and a dependency-owned
// value reached through a computed receiver binds its method there.
func TestGoSourceSprint165MethodDispatchOriginalAndNative(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "sprint165", "runtime-1", "method-dispatch", "original_and_native_receivers.go.txt"))
	if err != nil {
		t.Fatal(err)
	}
	differGoSource(t, string(data), nil, "")
}
