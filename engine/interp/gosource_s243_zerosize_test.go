//go:build full

package interp_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/go-quicktest/qt"
)

// Sprint: #243; Story: #674; Story-ID: 63073886bfce
func TestS243OriginalZeroSizeIdentityThreeModes(t *testing.T) {
	source, err := os.ReadFile(filepath.Join(runtime.GOROOT(), "test", "zerosize.go"))
	qt.Assert(t, qt.IsNil(err))
	typedSendThreeModes(t, string(source))
}

// Sprint: #243; Story: #674; Story-ID: 63073886bfce
func TestS243ZeroSizeIdentityNegativeControls(t *testing.T) {
	out, stderr, err := runGoSourcePointerConv(t, `package main
import "fmt"
type Box struct { lead int; zero [0]int }
func main() {
 a, b := new(int), new(int)
 var nilZero *[0]int
 zero := new([0]int)
 left, right := new(Box), new(Box)
 fmt.Println(a != b, nilZero == nil, zero != nil)
 fmt.Println(left != right, &left.zero != &right.zero)
}
`)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "true true true\ntrue true\n"))
}

// Sprint: #243; Story: #674; Story-ID: 63073886bfce
func TestS243ZeroSizePointerMapEqualityThreeModes(t *testing.T) {
	typedSendThreeModes(t, `package main

type Zero [0]int
type Key struct { Pointer *Zero }
type Embedded struct { *Zero }

func check(ok bool) {
	if !ok { panic("pointer equality and map key identity disagree") }
}

func main() {
	a, b := new(Zero), new(Zero)
	equal := a == b

	direct := map[*Zero]int{a: 1, b: 2}
	check((len(direct) == 1) == equal)
	check(direct[b] == 2)
	delete(direct, a)
	_, found := direct[b]
	check(found != equal)

	var ia, ib any = a, b
	interfaces := map[any]int{ia: 1, ib: 2}
	check((ia == ib) == equal)
	check((len(interfaces) == 1) == equal)
	check(interfaces[ib] == 2)
	delete(interfaces, ia)
	_, found = interfaces[ib]
	check(found != equal)

	ka, kb := Key{a}, Key{b}
	aggregates := map[Key]int{ka: 1, kb: 2}
	check((ka == kb) == equal)
	check((len(aggregates) == 1) == equal)
	check(aggregates[kb] == 2)
	delete(aggregates, ka)
	_, found = aggregates[kb]
	check(found != equal)

	ea, eb := Embedded{a}, Embedded{b}
	embedded := map[Embedded]int{ea: 1, eb: 2}
	check((ea == eb) == equal)
	check((len(embedded) == 1) == equal)

	nonzeroA, nonzeroB := new(int), new(int)
	nonzero := map[*int]int{nonzeroA: 1, nonzeroB: 2}
	check(nonzeroA != nonzeroB && len(nonzero) == 2)
	var nilZero *Zero
	nils := map[*Zero]int{nilZero: 1}
	check(nilZero == nil && nils[nil] == 1)
}
`)
}
