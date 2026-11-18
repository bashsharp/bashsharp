//go:build full

package interp_test

import "testing"

// Sprint: #281; Story: #809; Story-ID: fac7e14af4a8
//
// A pointer read from a global slice can be boxed into an interface field in an
// elided pointer-to-struct map literal. This is the small shape go/types'
// typeterm table reaches: map[string]*term{"int": {false, Typ[Int]}}, where
// the second field is an interface and Typ is a global []*Basic.
func TestS281InterfaceFieldFromIndexedPointer(t *testing.T) {
	source := `package main

import "fmt"

type Type interface {
	String() string
}

type Basic struct {
	Name string
}

func (b *Basic) String() string { return b.Name }

var Typ = []*Basic{{Name: "int"}}

type term struct {
	flag bool
	typ  Type
}

var terms = map[string]*term{
	"int": {false, Typ[0]},
}

func main() {
	fmt.Println(terms["int"].typ.String())
}
`
	differGoSource(t, source, nil, "")
}

// Sprint: #281; Story: #809; Story-ID: fac7e14af4a8
//
// Boxing an indexed or sliced value into an interface must consume the source
// expression once. Replaying the scalar fallback calls index and bound
// functions twice, which diverges from native Go and can change the selected
// element or bounds.
func TestS281IndexBoxEvaluatesOnce(t *testing.T) {
	tests := map[string]string{
		"scalar index": `a := []int{7}
	var v any = a[idx()]
	fmt.Println(v, count)`,
		"slice bound": `a := []int{7}
	var v any = a[idx():]
	fmt.Println(v, count)`,
		"pointer identity": `a := []*int{new(int)}
	var v any = a[idx()]
	fmt.Println(v == a[0], count)`,
		"typed nil pointer": `a := []*int{nil}
	var v any = a[idx()]
	var p *int
	fmt.Println(v == nil, v == p, count)`,
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			source := `package main

import "fmt"

var count int

func idx() int {
	count++
	return 0
}

func main() {
	` + body + `
}
`
			differGoSource(t, source, nil, "")
		})
	}
}
