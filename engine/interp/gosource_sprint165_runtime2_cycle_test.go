//go:build full

package interp_test

// Sprint: #165; Story: #99; Story-ID: ca559d7ee23d
//
// Cyclic pointees cross the bridge finitely, as back-references.

import (
	"os"
	"path/filepath"
	"testing"
)

func mustReadSprint165TransportCycle(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "sprint165", "runtime-2", "transport-cycle", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// list2's shape: a doubly linked ring with a sentinel, its elements handed
// to fmt by pointer (%p, %v, %T). Before the mechanism the transport walked
// the cycle until the native stack overflowed.
func TestGoSourceSprint165CyclicPointeeCrossesFinitely(t *testing.T) {
	differGoSource(t, mustReadSprint165TransportCycle(t, "ring.go.txt"), nil, "")
}

// Positive control: the acyclic form of the same structure (a chain whose
// ends are nil) crosses exactly as before — no back-reference is spelled.
func TestGoSourceSprint165AcyclicPointeeUnchanged(t *testing.T) {
	differGoSource(t, mustReadSprint165TransportCycle(t, "chain.go.txt"), nil, "")
}

// A pointer written back by the dependency through a cyclic pointee keeps
// pointer identity: the method the dependency calls back mutates the ring,
// and the interpreter's view after the call agrees with Go's.
func TestGoSourceSprint165CyclicPointeeWriteback(t *testing.T) {
	differGoSource(t, mustReadSprint165TransportCycle(t, "writeback.go.txt"), nil, "")
}
