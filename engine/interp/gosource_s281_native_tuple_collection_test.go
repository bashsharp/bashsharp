//go:build full

package interp_test

// Sprint: #281; Story: #809; Story-ID: fac7e14af4a8

import "testing"

// TestS281NativePopulatedIntFieldPreservesType exercises exported int and
// int64 fields on pointers populated inside the dependency worker. The fields
// must retain their declared identities when copied into ordinary scalars and
// when used as interpreter-owned map keys.
func TestS281NativePopulatedIntFieldPreservesType(t *testing.T) {
	const source = `package main
import (
	"encoding/json"
	"fmt"
	"image"
	"io"
)
func main() {
	point := new(image.Point)
	if err := json.Unmarshal([]byte("{\"X\":7524}"), point); err != nil { panic(err) }
	var x int = point.X
	seen := map[int]bool{}
	seen[point.X] = true
	reader := new(io.LimitedReader)
	if err := json.Unmarshal([]byte("{\"N\":9007199254740993}"), reader); err != nil { panic(err) }
	var n int64 = reader.N
	wide := map[int64]bool{}
	wide[reader.N] = true
	fmt.Println(x, seen[7524], n, wide[9007199254740993])
}`
	differGoSource(t, source, nil, "")
}

// TestS281NativeTupleCollectionField compares the non-plain tuple assignment
// shape from debug/dwarf's readScope with native Go. The synthetic entry makes
// Data.Ranges return its literal low/high-PC pair before any optional DWARF
// section is read, so this needs no ELF or compiler fixture.
func TestS281NativeTupleCollectionField(t *testing.T) {
	const source = `package main
import (
	"debug/dwarf"
	"fmt"
)
type Ranges [][2]uint64
type Scope struct { ranges Ranges }
func main() {
	entry := &dwarf.Entry{Field: []dwarf.Field{
		{Attr: dwarf.AttrLowpc, Val: uint64(10)},
		{Attr: dwarf.AttrHighpc, Class: dwarf.ClassAddress, Val: uint64(20)},
	}}
	var scope Scope
	var err error
	scope.ranges, err = new(dwarf.Data).Ranges(entry)
	fmt.Println(err, len(scope.ranges), scope.ranges[0][0], scope.ranges[0][1])
}`
	differGoSource(t, source, nil, "")
}

// TestS281NativeTupleNestedSliceField covers the same result-temporary path
// when indexing the outer native handle produces another slice handle. The
// local alias checks that materialization retains interpreter slice aliasing.
func TestS281NativeTupleNestedSliceField(t *testing.T) {
	const source = `package main
import (
	"encoding/csv"
	"fmt"
	"strings"
)
type Scope struct { records [][]string }
func main() {
	var scope Scope
	var err error
	scope.records, err = csv.NewReader(strings.NewReader("a,b\nc,d\n")).ReadAll()
	alias := scope.records
	alias[0][0] = "changed"
	fmt.Println(err, len(scope.records), len(scope.records[0]), scope.records[0][0], alias[1][1])
}`
	differGoSource(t, source, nil, "")
}

// TestS281TupleCollectionAssignmentControls keeps the repair on the ordinary
// composite assignment path: local tuple values, nil and empty slices, arrays,
// and a later plain reassignment/range must retain their existing semantics.
func TestS281TupleCollectionAssignmentControls(t *testing.T) {
	const source = `package main
import (
	"crypto/sha256"
	"debug/dwarf"
	"fmt"
)
type Scope struct {
	ranges [][2]uint64
	sum [32]byte
}
func localNil() ([][2]uint64, error) { return nil, nil }
func localEmpty() ([][2]uint64, error) { return [][2]uint64{}, nil }
func localLiteral() ([][2]uint64, error) { return [][2]uint64{{1, 2}, {3, 4}}, nil }
func localArray() ([32]byte, error) { return sha256.Sum256([]byte("tuple")), nil }
func main() {
	var scope Scope
	var err error
	scope.ranges, err = localNil()
	fmt.Println(err, scope.ranges == nil, len(scope.ranges))
	scope.ranges, err = localEmpty()
	fmt.Println(err, scope.ranges == nil, len(scope.ranges))
	scope.ranges, err = localLiteral()
	fmt.Println(err, len(scope.ranges), scope.ranges[0], scope.ranges[1])
	scope.sum, err = localArray()
	fmt.Println(err, scope.sum[0], scope.sum[31])
	entry := &dwarf.Entry{Field: []dwarf.Field{
		{Attr: dwarf.AttrLowpc, Val: uint64(10)},
		{Attr: dwarf.AttrHighpc, Class: dwarf.ClassAddress, Val: uint64(20)},
	}}
	ranges, err := new(dwarf.Data).Ranges(entry)
	var reassigned [][2]uint64
	reassigned = ranges
	var total uint64
	for _, pair := range reassigned { total += pair[0] + pair[1] }
	fmt.Println(err, len(reassigned), total)
}`
	differGoSource(t, source, nil, "")
}

// TestS281NativeNilTupleCollectionField keeps dependency-produced typed nil
// slices on the same tuple-to-field path. Zero, explicit nil and all-local
// imported composites must be recognized from dwarf.Entry itself; no field
// value needs to establish native ownership. The non-range field is also the
// plain native-Go oracle and returns without consulting an ELF section.
func TestS281NativeNilTupleCollectionField(t *testing.T) {
	const source = `package main
import (
	"debug/dwarf"
	"fmt"
)
type Scope struct { ranges [][2]uint64 }
func localEmpty() [][2]uint64 { return [][2]uint64{} }
func check(entry *dwarf.Entry) {
	var scope Scope
	var err error
	scope.ranges, err = new(dwarf.Data).Ranges(entry)
	fmt.Println(err, scope.ranges == nil, len(scope.ranges))
}
func main() {
	check(&dwarf.Entry{})
	check(&dwarf.Entry{Field: nil})
	check(&dwarf.Entry{Field: []dwarf.Field{{Attr: dwarf.AttrName, Val: "no-ranges"}}})
	empty := localEmpty()
	fmt.Println(empty == nil, len(empty))
}`
	differGoSource(t, source, nil, "")
}

// TestS281ImportedCompositeBoundaryControls keeps local struct construction
// and imported scalar storage on their established interpreter paths while an
// imported aggregate with only local field expressions crosses natively.
func TestS281ImportedCompositeBoundaryControls(t *testing.T) {
	const source = `package main
import (
	"debug/dwarf"
	"fmt"
)
type localEntry struct { Field []string }
func local(e *localEntry) { fmt.Println(e.Field == nil, len(e.Field)) }
func imported(e *dwarf.Entry, off dwarf.Offset) {
	fmt.Println(e.Offset, e.Children, e.Field == nil, len(e.Field), off)
}
func importedValue(e dwarf.Entry) { fmt.Println(e.Offset, len(e.Field)) }
func main() {
	local(&localEntry{})
	local(&localEntry{Field: nil})
	imported(&dwarf.Entry{}, dwarf.Offset(3))
	imported(&dwarf.Entry{Offset: 7, Children: false, Field: nil}, dwarf.Offset(4))
	importedValue(dwarf.Entry{})
}`
	differGoSource(t, source, nil, "")
}

// TestS281NativeTupleCollectionRejectsIncompatibleTarget keeps comma-ok
// assertion failure behavior adjacent to the direct native-carrier controls
// in TestS281NativeSequenceMaterializationChecksSourceType.
func TestS281NativeTupleCollectionRejectsIncompatibleTarget(t *testing.T) {
	const source = `package main
import (
	"debug/dwarf"
	"fmt"
)
type Scope struct { ranges [][2]string }
func main() {
	entry := &dwarf.Entry{Field: []dwarf.Field{
		{Attr: dwarf.AttrLowpc, Val: uint64(10)},
		{Attr: dwarf.AttrHighpc, Class: dwarf.ClassAddress, Val: uint64(20)},
	}}
	ranges, err := new(dwarf.Data).Ranges(entry)
	var scope Scope
	var ok bool
	scope.ranges, ok = any(ranges).([][2]string)
	fmt.Println(err, ok, scope.ranges == nil, len(scope.ranges))
}`
	differGoSource(t, source, nil, "")
}
