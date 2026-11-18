//go:build full

package interp_test

import (
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/go-quicktest/qt"
)

func TestS219CollectionZero(t *testing.T) {
	const src = `package main
import "fmt"
func main() {
	var nils []int
	empty := []int{}
	var a [4]int
	fmt.Println(nils == nil, empty == nil, len(nils), cap(nils), len(empty), cap(empty))
	fmt.Println(a[0], a[3], len(a), cap(a))
}
`
	out, stderr, err := runGoSource(t, "s219collectionzero", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "true false 0 0 0 0\n0 0 4 4\n"))
}

func TestS219ArrayInference(t *testing.T) {
	const src = `package main
import "fmt"
var fixed [3]int
func main() {
	fixed = [...]int{1, 2, 3}
	p := &[...]byte{1, 2, 3, 4}
	var q *[4]byte = p
	fmt.Println(fixed, len(*q), cap(*q), q[3])
}
`
	out, stderr, err := runGoSource(t, "s219arrayinference", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "[1 2 3] 4 4 4\n"))

	_, stderr, err = runBashSharpCall(t, `func main() {
	var wrong [2]int = [...]int{1, 2, 3}
}
main()
`)
	qt.Assert(t, qt.ErrorIs(err, interp.ExitStatus(2)))
	if !strings.Contains(stderr, "BASHPP-ECOLLECTION-LENGTH") {
		t.Fatalf("stderr %q does not contain inferred-array length diagnostic", stderr)
	}
}
