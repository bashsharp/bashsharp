//go:build full

// Sprint: #209; Story: #460; Story-ID: d8e7d58f362b
//
// Barrier D owner-151 collection/builtin repairs, one focused positive per
// mechanism run through the Go-source front end (the mode the corpus measures):
//
//   - BASHPP-ECOLLECTION-ELEMENT: a large unsigned constant element (above
//     math.MaxInt64) is carried as its decimal spelling; it must store, read
//     and cross the dependency boundary as the integer it is (root issue2615).
//   - BASHPP-EBUILTIN-TYPE: the same carrier reached through a value builtin
//     (append) must be accepted, not rejected as a type error (root issue68227).
//   - BASHPP-ECOLLECTION-LENGTH: a constant array-length expression that is not
//     a plain integer literal — an integral float, or len(array) — folds to its
//     integer value (roots bug254, complit).
//   - BASHPP-ECOLLECTION-BOUNDS: an out-of-range indexed *assignment* faults as
//     Go's recoverable runtime panic, not a hard interpreter abort (roots
//     issue79197, issue79236).
package interp_test

import (
	"strings"
	"testing"

	"github.com/go-quicktest/qt"

	"github.com/bashsharp/bashsharp/engine/interp"
)

func TestStory460CollectionElementLargeUnsigned(t *testing.T) {
	// Positive: the max uint64 element stores and reads back exactly.
	src := "package main\nimport \"fmt\"\nfunc main() {\n\txs := []uint64{0, 1<<64 - 1}\n\tfmt.Println(xs[1])\n}\n"
	out, stderr, err := runGoSource(t, "story460elem", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "18446744073709551615\n"))
}

func TestStory460BuiltinTypeLargeUnsigned(t *testing.T) {
	// Positive: append carries the same large-unsigned element without an
	// EBUILTIN-TYPE rejection.
	src := "package main\nimport \"fmt\"\nfunc main() {\n\txs := []uint64{}\n\txs = append(xs, 1<<64-1)\n\tfmt.Println(xs[0])\n}\n"
	out, stderr, err := runGoSource(t, "story460builtin", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "18446744073709551615\n"))
}

func TestStory460ArrayLengthConstExpr(t *testing.T) {
	// Positive: an integral float length and a len(array) length both fold.
	src := "package main\nimport \"fmt\"\nfunc main() {\n\tvar b [1e1]int\n\ta := [...]int{1, 2, 3}\n\tvar c [len(a)]int\n\tfmt.Println(len(b), len(c))\n}\n"
	out, stderr, err := runGoSource(t, "story460length", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "10 3\n"))
}

func TestStory460IndexAssignBoundsPanic(t *testing.T) {
	// Positive: an out-of-range indexed assignment is a recoverable runtime
	// panic with Go's message, not a BASHPP-ECOLLECTION-BOUNDS abort.
	src := "package main\nimport \"fmt\"\nfunc main() {\n\tdefer func() {\n\t\tif r := recover(); r != nil {\n\t\t\tfmt.Println(\"recovered:\", r)\n\t\t}\n\t}()\n\ta := []int{1, 2, 3}\n\ti := 5\n\ta[i] = 9\n\tfmt.Println(\"unreachable\")\n}\n"
	out, stderr, err := runGoSource(t, "story460bounds", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "recovered: runtime error: index out of range [5] with length 3\n"))
}

// The classic (non Go-source) Bash# dialect keeps its established diagnostics:
// the large-unsigned carrier is not accepted and an out-of-range indexed
// assignment stays a hard BASHPP-ECOLLECTION-BOUNDS abort. These negatives
// guard the Go-source gating on the element and bounds repairs.

func TestStory460ClassicElementRejected(t *testing.T) {
	src := "func main() {\n x := []uint64{\"nope\"}\n _ := x[0]\n}\nmain()\n"
	_, stderr, err := runBashSharpCall(t, src)
	qt.Assert(t, qt.ErrorIs(err, interp.ExitStatus(2)))
	qt.Assert(t, qt.IsTrue(strings.HasPrefix(stderr, "BASHPP-ECOLLECTION-ELEMENT:")), qt.Commentf("stderr: %s", stderr))
}

func TestStory460ClassicAssignBoundsAbort(t *testing.T) {
	src := "func main() {\n x := []int{1}\n x[5] = 2\n}\nmain()\n"
	_, stderr, err := runBashSharpCall(t, src)
	qt.Assert(t, qt.ErrorIs(err, interp.ExitStatus(2)))
	qt.Assert(t, qt.IsTrue(strings.HasPrefix(stderr, "BASHPP-ECOLLECTION-BOUNDS:")), qt.Commentf("stderr: %s", stderr))
}
