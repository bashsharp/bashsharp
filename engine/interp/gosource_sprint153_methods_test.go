//go:build full

package interp_test

// Sprint: #153; Story: S153.2; Story-ID: 7f74c9ff55b9
//
// Full method mirror for materialised local types: every method with an
// expressible signature — exported or unexported, any result
// arity — is mirrored by the generalised protocol stub, so the dependency's
// reflect view presents the original method set and can invoke any mirrored
// method through the callback. Reproducers live under
// testdata/sprint153/method-mirror/; differSprint153 is in
// gosource_sprint153_bridge_test.go and refuseSprint153 in
// gosource_sprint153_reflect_test.go.

import (
	"os"
	"path/filepath"
	"testing"
)

// TestGoSourceBridgeMethodMirror covers the mirrored method set: reflect
// enumeration over a type whose methods were previously omitted, a
// dependency-invoked mirror with typed results (json.Marshaler), and the
// already-supported String mirror as the positive control.
func TestGoSourceBridgeMethodMirror(t *testing.T) {
	differSprint153(t, "method-mirror")
}

// TestGoSourceBridgeVariadicMethodMirror promotes the old explicit refusal to
// native equivalence now that the helper can express and bind variadic stubs.
func TestGoSourceBridgeVariadicMethodMirror(t *testing.T) {
	path := filepath.Join("testdata", "sprint153", "method-mirror", "variadic_method_refused.go.txt")
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	differGoSource(t, string(source), nil, "")
}
