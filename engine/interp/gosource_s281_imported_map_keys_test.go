//go:build full

package interp_test

// Sprint: #281; Story: #810; Story-ID: 48c1146a3ab0
//
// Map keys of dependency-owned defined types with basic underlying types.
// A native-bridged import registers its types only in export metadata, not
// in the interpreter's declaration registry, so hashing a map key of such a
// type must normalize through that metadata: the key's shape is the basic
// underlying kind, and its identity is the import path rather than the
// per-file alias a lowered file spells the type with (interpreting
// cmd/compile/internal/ssagen failed with BASHPP-ECOLLECTION-KEY:
// unsupported map key type __gosource_import_0_10_15.Op).

import (
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/go-quicktest/qt"
)

// A map keyed by an imported defined integer type (time.Month) supports the
// full mechanism set: literal, store, lookup, overwrite, comma-ok, compound
// update, delete and len.
func TestS281ImportedNamedBasicMapKey(t *testing.T) {
	out, stderr := runGoSourcePackageSet(t, "s281-imported-basic-key", `package main
import ("fmt"; "time")
func main() {
	m := map[time.Month]string{time.January: "jan"}
	m[time.March] = "mar"
	m[time.January] = "JAN"
	v, ok := m[time.March]
	_, miss := m[time.May]
	fmt.Println(m[time.January], v, ok, miss, len(m))

	n := map[time.Month]int{time.April: 4}
	n[time.April] += 10
	n[time.June] += 6
	fmt.Println(n[time.April], n[time.June], len(n))

	sum, total := 0, 0
	for k, v := range n {
		sum += int(k)
		total += v
	}
	fmt.Println(sum, total)

	delete(n, time.April)
	_, still := n[time.April]
	fmt.Println(still, len(n))
}
`, nil)
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "JAN mar true false 2\n14 6 2\n10 20\nfalse 1\n"))
}

// A converted value and the dependency's own constant of one imported
// defined type are the same key; a different imported defined type over the
// same basic kind and value is not probed as that key's storage text.
func TestS281ImportedNamedBasicKeyConversionIdentity(t *testing.T) {
	out, stderr := runGoSourcePackageSet(t, "s281-imported-key-conversion", `package main
import ("fmt"; "time")
func main() {
	m := map[time.Month]string{time.March: "mar"}
	fmt.Println(m[time.Month(3)], len(m))
	m[time.Month(7)] = "jul"
	fmt.Println(m[time.July], len(m))
}
`, nil)
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "mar 1\njul 2\n"))
}

// The ssagen shape: a locally declared struct key whose fields are imported
// defined basic types (opAndType{ir.Op, types.Kind}), hashed field by field
// through the same metadata normalization, including when the map crosses a
// package boundary and the far side spells the field types with its own
// import aliases.
func TestS281ImportedFieldStructMapKey(t *testing.T) {
	helper := gosource.PackageSpec{
		Path: "test/helper",
		Sources: []gosource.Source{{
			Name: "helper.go",
			Data: []byte(`package helper
import "time"
type Key struct {
	M time.Month
	D time.Duration
}
func Get(m map[Key]string, k Key) string { return m[k] }
func Bump(m map[Key]int, k Key) { m[k]++ }
`),
		}},
	}
	out, stderr := runGoSourcePackageSet(t, "s281-imported-field-struct-key", `package main
import ("fmt"; "time"; "./helper")
func main() {
	m := map[helper.Key]string{{M: time.March, D: 5 * time.Second}: "hit"}
	k := helper.Key{M: time.March, D: 5 * time.Second}
	fmt.Println(m[k], helper.Get(m, k), len(m))

	n := map[helper.Key]int{k: 40}
	helper.Bump(n, k)
	helper.Bump(n, k)
	fmt.Println(n[k], len(n))
}
`, []gosource.PackageSpec{helper})
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "hit hit 1\n42 1\n"))
}

// An interface-keyed map hashes the dynamic type together with the value:
// an imported defined integer type, its basic kind, and a second imported
// defined type over the same kind and numeric value are three distinct keys.
func TestS281ImportedDynamicTypeInterfaceKey(t *testing.T) {
	out, stderr := runGoSourcePackageSet(t, "s281-imported-dynamic-key", `package main
import ("fmt"; "time")
func main() {
	m := map[any]string{}
	m[time.March] = "month"
	m[3] = "int"
	m[time.Duration(3)] = "duration"
	fmt.Println(m[time.March], m[3], m[time.Duration(3)], len(m))
	delete(m, time.March)
	fmt.Println(m[time.March] == "", len(m))
}
`, nil)
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "month int duration 3\ntrue 2\n"))
}

// Accepting imported defined basic types must not widen hashing past
// comparability: an imported defined type over a non-comparable underlying
// type (net.IP, a []byte) still panics as an interface key.
func TestS281ImportedUnhashableDynamicTypeStillPanics(t *testing.T) {
	out, stderr := runGoSourcePackageSet(t, "s281-imported-unhashable-key", `package main
import ("fmt"; "net")
func main() {
	defer func() {
		fmt.Println("recovered:", recover())
	}()
	m := map[any]int{}
	m[net.ParseIP("127.0.0.1")] = 1
	fmt.Println("unreachable", len(m))
}
`, nil)
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "recovered: runtime error: hash of unhashable type net.IP\n"))
}

// A map keyed by an interpreted dependency's defined basic type keeps
// working across the package boundary: the declaration registry resolves
// the alias-qualified name, and the far side's own alias spelling reads and
// updates the entries the near side wrote.
func TestS281InterpretedDependencyNamedBasicMapKey(t *testing.T) {
	dep := gosource.PackageSpec{
		Path: "test/dep",
		Sources: []gosource.Source{{
			Name: "dep.go",
			Data: []byte(`package dep
type Op int32
const (
	OADD Op = iota + 1
	OSUB
)
type Name string
`),
		}},
	}
	helper := gosource.PackageSpec{
		Path: "test/helper",
		Sources: []gosource.Source{{
			Name: "helper.go",
			Data: []byte(`package helper
import "./dep"
func Get(m map[dep.Op]string, k dep.Op) string { return m[k] }
func Bump(m map[dep.Op]int, k dep.Op) { m[k] += 1 }
`),
		}},
	}
	out, stderr := runGoSourcePackageSet(t, "s281-interpreted-dep-key", `package main
import ("fmt"; "./dep"; "./helper")
func main() {
	m := map[dep.Op]string{dep.OADD: "add"}
	m[dep.OSUB] = "sub"
	fmt.Println(helper.Get(m, dep.OADD), m[dep.OSUB], len(m))

	n := map[dep.Op]int{dep.OADD: 10}
	helper.Bump(n, dep.OADD)
	helper.Bump(n, dep.OADD)
	fmt.Println(n[dep.OADD], len(n))

	names := map[dep.Name]int{dep.Name("x"): 1}
	names[dep.Name("y")] = 2
	fmt.Println(names[dep.Name("x")], names[dep.Name("y")], len(names))
}
`, []gosource.PackageSpec{dep, helper})
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "add sub 2\n12 1\n1 2 2\n"))
}
