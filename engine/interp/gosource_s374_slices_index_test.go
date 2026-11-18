//go:build full

package interp_test

// Sprint: #374; Story-ID: 5568f906f766

import "testing"

// types2 and go/types look a type parameter up in its list with
// `slices.Index(list, tpar)`: a slice of pointers to a declared type that has
// methods. slices.Index only reads the slice and compares elements with ==,
// so no method can run, yet the call was refused as "original callback with
// copied slice references is unsupported" because the elements' type carries
// callbacks. Pointer elements compare by identity, never by pointee.
func TestS374SlicesIndexOverPointersToMethodTypes(t *testing.T) {
	const source = `package main

import (
	"fmt"
	"slices"
)

type TypeParam struct {
	name  string
	bound *TypeParam
}

func (t *TypeParam) String() string { return t.name }

type list struct{ tparams []*TypeParam }

func (l *list) all() []*TypeParam {
	if l == nil {
		return nil
	}
	return l.tparams
}

func main() {
	a, b, c := &TypeParam{name: "A"}, &TypeParam{name: "B"}, &TypeParam{name: "C"}
	b.bound = a
	a.bound = b
	twin := &TypeParam{name: "B", bound: a}
	l := &list{tparams: []*TypeParam{a, b, nil}}
	fmt.Println(slices.Index(l.all(), a), slices.Index(l.all(), b), slices.Index(l.all(), c), slices.Index(l.all(), twin))
	fmt.Println(slices.Index(l.all(), nil), slices.Contains(l.all(), b), slices.Contains(l.all(), c))
	var none *list
	fmt.Println(slices.Index(none.all(), a), slices.Contains(none.all(), a))
	if i := slices.Index(l.tparams, b); i >= 0 {
		fmt.Println(l.tparams[i], l.tparams[i].bound)
	}
	fmt.Println(slices.Index([]string{"x", "y"}, "y"), slices.Contains([]int{1, 2, 3}, 4))
}
`
	got, err := runGoSourceIdentity(t, source, "")
	want := "0 1 -1 -1\n2 true false\n-1 false\nB A\n1 false\n"
	if err != nil || got.stdout != want || got.stderr != "" || got.status != 0 {
		t.Fatalf("outcome=%+v; err=%v", got, err)
	}
}
