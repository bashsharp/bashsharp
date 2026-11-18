//go:build full

package interp_test

// Sprint: #153; Story: S153.2; Story-ID: 7f74c9ff55b9
//
// Retained function callbacks with pointer parameters (runtime.SetFinalizer)
// were refused by design here: a bridge mirror cannot reproduce the liveness
// of interpreter storage (testdata/sprint153/bridge/FINDINGS-S153.2.md).
// Sprint 248 (Story 424) keeps the finalizer on the interpreter's own
// allocation instead (gosource_finalizer.go), so the reproducer now runs; the
// shape the interpreter still cannot represent — a finalizer on an interior
// pointer — keeps the prompt refusal.

import (
	"os"
	"path/filepath"
	"testing"
)

// TestGoSourceBridgeRetainedFinalizerRefusal: the former reproducer arms a
// finalizer on an object that is dead after SetFinalizer, so the collection
// delivers it on its own goroutine before "done" is printed, which Go allows.
func TestGoSourceBridgeRetainedFinalizerRefusal(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("testdata", "sprint153", "retained-callback", "setfinalizer_refused.go.txt"))
	if err != nil {
		t.Fatal(err)
	}
	out, stderr, err := runGoSource(t, "setfinalizer", string(source))
	if err != nil || stderr != "" || out != "finalized 7\ndone\n" {
		t.Fatalf("run=%v stdout=%q stderr=%q", err, out, stderr)
	}
	refuseSprint153(t, "retained-callback", "setfinalizer_interior_refused.go.txt",
		"original callback signature requires value-semantics parameters")
}
