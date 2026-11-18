//go:build full

package interp_test

// Sprint: #281; Story: #810; Story-ID: 48c1146a3ab0

import (
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
)

// Go 1.20 widened constraint satisfaction so an ordinary interface may be a
// type argument for comparable. The imported alias is the exact shape used by
// cmd/compile/internal/types2: its local Type aliases an ordinary interface in
// cmd/compile/internal/syntax, whose declaration lives in export metadata.
// Comparable dynamic values work normally; an identical non-comparable dynamic
// type is admitted statically and panics only when the comparison executes.
func TestGoSourceS281ComparableOrdinaryInterfaces(t *testing.T) {
	const source = `package main

import "fmt"

type localType interface { String() string }
type importedType = fmt.Stringer

type number int
func (n number) String() string { return fmt.Sprint(int(n)) }

type numbers []int
func (n numbers) String() string { return fmt.Sprint([]int(n)) }

func substList[T comparable](in []T, subst func(T) T) (out []T) {
	for i, old := range in {
		new := subst(old)
		if new != old {
			if out == nil { out = append([]T(nil), in...) }
			out[i] = new
		}
	}
	return
}

func unchanged[T comparable](v T) T { return v }

func dynamicPanic() {
	defer func() { fmt.Println("panic", recover() != nil) }()
	var value importedType = numbers{1, 2}
	_ = substList([]importedType{value}, unchanged[importedType])
}

func main() {
	var local localType = number(7)
	var imported importedType = number(8)
	fmt.Println(substList([]localType{local}, unchanged[localType]) == nil)
	fmt.Println(substList([]importedType{imported}, unchanged[importedType]) == nil)
	dynamicPanic()
}
`
	differGoSource(t, source, nil, "")
}

// A mapped package prefixes its local alias before evaluation. This keeps the
// types2 failure's full metadata path: __gosource_pkg_0_Type resolves to an
// imported ordinary interface rather than to an interpreter declaration.
func TestGoSourceS281MappedComparableOrdinaryInterface(t *testing.T) {
	out, stderr, err := runGoSourcePackages(t, `package main
import "test/p"
func main() { p.Run() }
`, map[string]string{"a.go": `package p

import "fmt"

type Type interface { String() string }
type Imported = fmt.Stringer

type number int
func (n number) String() string { return fmt.Sprint(int(n)) }

type numbers []int
func (n numbers) String() string { return fmt.Sprint([]int(n)) }

func substList[T comparable](in []T, subst func(T) T) (out []T) {
	for i, old := range in {
		new := subst(old)
		if new != old {
			if out == nil { out = append([]T(nil), in...) }
			out[i] = new
		}
	}
	return
}

func unchanged[T comparable](v T) T { return v }

func Run() {
	var local Type = number(7)
	var imported Imported = number(8)
	fmt.Println(substList([]Type{local}, unchanged[Type]) == nil)
	fmt.Println(substList([]Imported{imported}, unchanged[Imported]) == nil)
	func() {
		defer func() { fmt.Println("panic", recover() != nil) }()
		var value Imported = numbers{1, 2}
		_ = substList([]Imported{value}, unchanged[Imported])
	}()
}
`})
	if err != nil || stderr != "" || out != "true\ntrue\npanic true\n" {
		t.Fatalf("err=%v stdout=%q stderr=%q", err, out, stderr)
	}
}

// The interface exception does not widen comparable to statically
// non-comparable types. Go rejects each instantiation before its body can run.
func TestGoSourceS281ComparableStillRejectsStaticTypes(t *testing.T) {
	tests := map[string]string{
		"slice": "[]int",
		"map":   "map[string]int",
		"func":  "func()",
	}
	for name, typ := range tests {
		t.Run(name, func(t *testing.T) {
			source := `package main
func accept[T comparable](T) {}
func main() { var value ` + typ + `; accept[` + typ + `](value) }
`
			_, err := gosource.Parse(strings.NewReader(source), name+".go", gosource.Options{RunMain: true})
			if err == nil || (!strings.Contains(err.Error(), "does not satisfy comparable") &&
				!strings.Contains(err.Error(), "not comparable")) {
				t.Fatalf("expected comparable rejection for %s, got %v", typ, err)
			}
		})
	}
}
