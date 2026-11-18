//go:build full

package interp_test

// Sprint: #209; Story: #461; Story-ID: 4ed649697945
//
// A map is a reference value, so a map operand that names no assignable
// variable -- `f()[k] += v`, `f()[k]++` (test/fixedbugs/bug196.go) -- is still
// a legal Go update target: Go evaluates the operand exactly once and mutates
// the map it returned in place. The compound-update router used to require a
// scalar-typed operand and then an assignable root, so a map returned by a
// call fell through to the addressability walk and was rejected with
// BASHPP-EUPDATE-TARGET / BASHPP-ENONADDRESSABLE. It now resolves the callee's
// single result type, reads the operand once, and stores back into the
// returned map by read-modify-write.
//
// The invariant the repair preserves: a map ELEMENT is still not addressable.
// `&f()[k]` remains illegal Go, so no program can smuggle a map element's
// address past go/types (TestStory461MapElementAddressStillRejected). The
// readonly guard also still fires when the operand does name a variable
// (TestStory461RootedReadonlyMapStillGuarded).
//
// Out of scope -- distinct causes, not this update operation:
//   - map.go `mipT[i].i += 1` updates a struct field through a *pointer* held
//     in a map element; that is the pointer-addressability path.
//   - metrics.go `m.m[key2[T1,T2]{v1,v2}]++` fails typing the generic
//     composite-literal key against the instantiated map key type
//     (BASHPP-ESTRUCT-TYPE), a generic-substitution cause.

import (
	"strings"
	"testing"

	"github.com/go-quicktest/qt"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// bug196.go's shape: the map is returned by a function with a visible side
// effect, so the call count is the load-bearing assertion -- the operand is
// evaluated exactly once even though it appears in a read-modify-write.
func TestStory461MapUpdateFromCallResult(t *testing.T) {
	src := `package main
import "fmt"
var m = map[int]int{0: 0, 1: 0}
var calls = 0
func f() map[int]int { calls++; return m }
func main() {
	f()[0]++
	f()[1] += 2
	fmt.Println(m[0], m[1], calls)
}`
	out, stderr, err := runGoSource(t, "story461mapcall", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "1 2 2\n"))
}

// A string element proves the path is a genuine read-modify-write of the
// returned map rather than an integer-only arithmetic shortcut.
func TestStory461MapUpdateFromCallResultString(t *testing.T) {
	src := `package main
import "fmt"
var shared = map[string]string{"a": "x"}
func table() map[string]string { return shared }
func main() {
	table()["a"] += "y"
	fmt.Println(shared["a"])
}`
	out, stderr, err := runGoSource(t, "story461mapstr", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "xy\n"))
}

// Map elements stay non-addressable: the read-modify-write repair never made
// `&f()[k]` legal, and go/types refuses it before the evaluator ever runs.
func TestStory461MapElementAddressStillRejected(t *testing.T) {
	src := `package main
var m = map[int]int{0: 0}
func f() map[int]int { return m }
func main() {
	p := &f()[0]
	_ = p
}`
	_, err := gosource.Parse(strings.NewReader(src), "story461mapneg.go", gosource.Options{RunMain: true})
	qt.Assert(t, qt.IsNotNil(err))
	qt.Assert(t, qt.StringContains(err.Error(), "cannot take address"))
}

// Relaxing the root requirement must not drop the readonly guard where a root
// does exist: a readonly map is still refused and left unmutated.
func TestStory461RootedReadonlyMapStillGuarded(t *testing.T) {
	const src = `func main() {
 m := map[string]int{"k": 1}
 readonly m
 m["k"] += 1
 printf ':%s' m["k"]
}
main()
`
	var out strings.Builder
	r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP), interp.WithBashCompatErrors(true))
	bashPPRun(t, r, src)
	qt.Assert(t, qt.StringContains(out.String(), "BASHPP-EUPDATE-WRITE"))
	qt.Assert(t, qt.StringContains(out.String(), "BASHPP-EREADONLY-MUTATION"))
	qt.Assert(t, qt.StringContains(out.String(), ":1"))
}
