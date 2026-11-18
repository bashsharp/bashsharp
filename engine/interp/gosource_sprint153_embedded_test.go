//go:build full

package interp_test

// Sprint: #153; Story: S153.2; Story-ID: 7f74c9ff55b9
//
// Embedded-field transport. A local struct with an embedded field used to be
// unmaterialisable (named types) or spelled with an <unsupported embedded
// field> placeholder (anonymous shapes). The helper now emits real embedding
// of the rendered element type, the host transports the embedded storage
// under its promoted name, and the generated codecs read and write that same
// storage. Reproducers live under testdata/sprint153/embedded-transport/.

import "testing"

// TestGoSourceBridgeEmbeddedTransport covers a named local struct with an
// embedded method-bearing type (promotion included), an anonymous struct
// embedding a function-local type, and a flat struct as positive control.
func TestGoSourceBridgeEmbeddedTransport(t *testing.T) {
	differSprint153(t, "embedded-transport")
}

// The former TestGoSourceBridgeEmbeddedRefusal reproducer (a struct embedding
// G[int]) is materialised since 0841fde8 made package-level generics public:
// it now runs under TestGoSourceBridgeEmbeddedTransport as
// generic_embedded.go and is compared with the native Go oracle.
