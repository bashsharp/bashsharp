//go:build full

package interp_test

// Sprint: #153; Story: S153.2; Story-ID: 7f74c9ff55b9
//
// Interface-identity scalars. An interpreted ...any variadic re-spread into
// an imported call sends each scalar with the declared interface{} identity;
// the worker used to resolve that identity to the interface type and fail
// ("integer for interface {}"). A scalar carrying a declared interface
// identity now decodes at its own kind and stays assignability-checked
// against the interface target. Reproducers live under
// testdata/sprint153/spread-identity/.

import "testing"

// TestGoSourceBridgeSpreadIdentity covers an original variadic ...any
// function re-spreading its arguments into fmt, with a direct fmt call as
// the already-supported positive control.
func TestGoSourceBridgeSpreadIdentity(t *testing.T) {
	differSprint153(t, "spread-identity")
}
