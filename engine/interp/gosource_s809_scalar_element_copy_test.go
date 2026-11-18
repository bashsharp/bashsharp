//go:build full

package interp_test

// Sprint: #281; Story: #809; Story-ID: fac7e14af4a8
//
// A scalar element read out of a materialized dependency collection carries
// its declared element type as metadata, not a payload shape. Copying one into
// a short-declared variable must produce a plain scalar: cmd/compile/internal/
// dwarfgen's TestScopeRanges walks `for pc := r[0]; pc < r[1]; pc++` over the
// [][2]uint64 that debug/dwarf's Data.Ranges returns, and every operator in
// that loop rejects an object cell.

import "testing"

// TestS809BridgeScalarElementLoop is the dwarfgen scope-range loop itself.
func TestS809BridgeScalarElementLoop(t *testing.T) {
	const source = `package main
import (
	"debug/dwarf"
	"fmt"
)
type Scope struct { ranges [][2]uint64 }
func main() {
	entry := &dwarf.Entry{Field: []dwarf.Field{
		{Attr: dwarf.AttrLowpc, Val: uint64(10)},
		{Attr: dwarf.AttrHighpc, Class: dwarf.ClassAddress, Val: uint64(14)},
	}}
	var scope Scope
	var err error
	scope.ranges, err = new(dwarf.Data).Ranges(entry)
	if err != nil { panic(err) }
	var total uint64
	for _, r := range scope.ranges {
		for pc := r[0]; pc < r[1]; pc++ {
			total += pc
		}
	}
	fmt.Println(len(scope.ranges), total)
}`
	differGoSource(t, source, nil, "")
}

// TestS809BridgeScalarElementOperators keeps every operator the copied scalar
// has to satisfy on the same binding: update, comparison, conversion,
// formatting, map-key use and a length argument.
func TestS809BridgeScalarElementOperators(t *testing.T) {
	const source = `package main
import (
	"debug/dwarf"
	"fmt"
)
type Scope struct { ranges [][2]uint64 }
func main() {
	entry := &dwarf.Entry{Field: []dwarf.Field{
		{Attr: dwarf.AttrLowpc, Val: uint64(10)},
		{Attr: dwarf.AttrHighpc, Class: dwarf.ClassAddress, Val: uint64(14)},
	}}
	var scope Scope
	var err error
	scope.ranges, err = new(dwarf.Data).Ranges(entry)
	if err != nil { panic(err) }
	r := scope.ranges[0]

	pc := r[0]
	pc++
	pc += 5
	pc -= 2
	pc--
	fmt.Println(pc)

	lo := r[0]
	fmt.Println(int(lo), float64(lo), uint32(lo), fmt.Sprintf("%d %v %T", lo, lo, lo))

	var q uint64 = lo
	q++
	fmt.Println(lo == 10, lo != q, lo < q, lo*2, lo%3, q)

	keys := map[uint64]string{lo: "low"}
	fmt.Println(keys[10], len(keys), len(make([]int, lo-7)))
}`
	differGoSource(t, source, nil, "")
}

// TestS809BridgeScalarFieldCopy takes the same repair through the selector
// short declaration. A value receiver rebuilt from the dependency's transport
// wrapper carries one scalar metadata per field, so `n := p.X` inside the
// callback body binds field metadata rather than an index element.
func TestS809BridgeScalarFieldCopy(t *testing.T) {
	const source = `package main
import "fmt"
type Pt struct{ X, Y int; Name string }
func (p Pt) String() string {
	n := p.X
	n++
	s := p.Name
	s += "!"
	return fmt.Sprint(n, " ", p.Y, " ", s)
}
func main() { fmt.Println(Pt{3, 4, "pt"}) }`
	differGoSource(t, source, nil, "")
}

// TestS809ScalarElementCopyControls keeps the interpreter-owned equivalents on
// their established paths: an element copied out of a local slice of arrays
// must behave identically, and a copied aggregate must still be an aggregate.
func TestS809ScalarElementCopyControls(t *testing.T) {
	const source = `package main
import "fmt"
func main() {
	local := [][2]uint64{{10, 14}, {20, 22}}
	var total uint64
	for _, r := range local {
		for pc := r[0]; pc < r[1]; pc++ { total += pc }
	}
	pair := local[1]
	pair[0] = 99
	names := [][]string{{"a", "b"}}
	row := names[0]
	row[1] = "changed"
	fmt.Println(total, pair, local[1], row, names[0])
}`
	differGoSource(t, source, nil, "")
}
