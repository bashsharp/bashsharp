//go:build full

package interp_test

// Sprint: #374; Story: #1527; Story-ID: dd26e98fbeee

import "testing"

// A single-value short declaration from an interface-element collection
// keeps the interface edge: the declared variable has the element's
// interface type, not the dynamic type of the value it happens to hold.
// Losing the edge makes a later type switch refuse the variable and a
// later interface box mint the interface itself as the dynamic type, as in
// cmd/compile/internal/types2 subst.go's (*subster).typ default arm
// (panic: unreachable) reached through substMap.lookup and cloneVar.
func TestS374SingleValueIndexReadKeepsInterface(t *testing.T) {
	const source = `package main

import "fmt"

type I interface{ String() string }

type B struct{ s string }

func (b *B) String() string { return b.s }

func describe(v I) string {
	switch t := v.(type) {
	case nil:
		return "nil"
	case *B:
		return "B:" + t.s
	default:
		_ = t
		return "mismatch"
	}
}

type substMap map[*B]I

func (m substMap) lookup(p *B) I {
	if t := m[p]; t != nil {
		return t
	}
	return p
}

type Box struct{ v I }

func main() {
	fresh := []*B{{s: "x"}}
	m := map[string]I{}
	m["k"] = fresh[0]
	t := m["k"]
	fmt.Println("nil:", t == nil)
	fmt.Println("direct:", describe(t))
	s := []I{nil}
	s[0] = fresh[0]
	u := s[0]
	fmt.Println("slice:", describe(u))
	key := &B{s: "q"}
	smap := substMap{key: fresh[0]}
	fmt.Println("lookup:", describe(smap.lookup(key)))
	fmt.Println("miss:", describe(smap.lookup(&B{s: "z"})))
	box := Box{}
	box.v = t
	fmt.Println("field:", describe(box.v))
}
`
	got, err := runGoSourceIdentity(t, source, "")
	want := "nil: false\ndirect: B:x\nslice: B:x\nlookup: B:x\nmiss: B:z\nfield: B:x\n"
	if err != nil || got.stdout != want || got.stderr != "" || got.status != 0 {
		t.Fatalf("outcome=%+v; err=%v", got, err)
	}
}
