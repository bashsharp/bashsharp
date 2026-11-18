//go:build full

package interp_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Sprint 165 runtime lane — registration of bridge types by identity. Each
// fixture under testdata/sprint165/runtime-1/bridge-identity runs unchanged
// through native Go and through the Runner; the native outcome is the
// expected value, status and (absence of) stray output.

func mustReadSprint165BridgeIdentity(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "sprint165", "runtime-1", "bridge-identity", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// Instantiations reached only through generic functions, generic method
// bodies and nested instantiations resolve under the identity the value
// carries at run time.
func TestGoSourceSprint165ReachedInstantiations(t *testing.T) {
	differGoSource(t, mustReadSprint165BridgeIdentity(t, "reached_instantiations.go.txt"), nil, "")
}

// Positive control: instantiations spelled at their use sites.
func TestGoSourceSprint165SpelledInstantiations(t *testing.T) {
	differGoSource(t, mustReadSprint165BridgeIdentity(t, "spelled_instantiations.go.txt"), nil, "")
}

// Identities the dependency handed over — an interface type of a package the
// program never imported, a pointer to an imported type — resolve when the
// interpreter sends them back.
func TestGoSourceSprint165HandedIdentities(t *testing.T) {
	differGoSource(t, mustReadSprint165BridgeIdentity(t, "handed_identities.go.txt"), nil, "")
}

// Negative: an identity nobody registered still fails closed. The value's
// declared type names a local type the helper refuses to materialise (a
// reserved helper identifier), and no reached instantiation or handed
// identity can stand in for it.
func TestGoSourceSprint165UnregisteredIdentityStillRefused(t *testing.T) {
	got := runGoSourceRunnerError(t, `package main

import "fmt"

func main() {
	// A function-local type named like a helper identifier stays
	// unmaterialised; the package-level spelling is registered under a
	// generated identity since Sprint 290 (bashpp_s290_reserved_type_names.go).
	type symbols struct{ N int }
	fmt.Println(symbols{N: 1})
}
`)
	if !strings.Contains(got, "unregistered bridge type") {
		t.Fatalf("an unmaterialised local type must still be refused, got %q", got)
	}
}

// A pointer to an interface variable crosses as a pointer to the interface.
func TestGoSourceSprint165InterfacePointee(t *testing.T) {
	differGoSource(t, mustReadSprint165BridgeIdentity(t, "interface_pointee.go.txt"), nil, "")
}
