//go:build full

package interp_test

// Sprint: #374; Story: #1512; Story-ID: 5137f4b98e0c

import "testing"

// Dereferencing a pointer to an interface yields the interface value: its
// dynamic type and nilness travel with the read. Dropping the interface edge
// makes a nil interface miss `case nil` in a type switch and a live one miss
// its concrete case, as in check.subst over a substituted signature during
// generic inference (cmd/compile/internal/types2 subst.go:90:
// panic: unreachable).
func TestS374PointerDerefInterfaceValue(t *testing.T) {
	const source = `package main

import "fmt"

type Iface interface{ M() string }

type T struct{ name string }

func (t *T) M() string { return t.name }

type Qualifier func(*Package) string

type Package struct{ path string }

func (p *Package) Path() string { return p.path }

func qual(pkg *Package) string { return pkg.path }

func describe(x any) string {
	switch t := x.(type) {
	case nil:
		return "nil"
	case *T:
		return "T:" + t.name
	case Qualifier:
		if t == nil {
			return "nilfunc"
		}
		return "qual"
	default:
		_ = t
		return "unknown"
	}
}

func main() {
	var v Iface
	p := &v
	fmt.Println("nil-compare:", *p == nil)
	var a any = *p
	fmt.Println("nil-switch:", describe(a))
	t2 := &T{name: "nn"}
	var w Iface = t2
	r := &w
	fmt.Println("live-compare:", *r != nil)
	var b any = *r
	fmt.Println("live-switch:", describe(b))
}
`
	got, err := runGoSourceIdentity(t, source, "")
	want := "nil-compare: true\nnil-switch: nil\nlive-compare: true\nlive-switch: T:nn\n"
	if err != nil || got.stdout != want || got.stderr != "" || got.status != 0 {
		t.Fatalf("outcome=%+v; err=%v", got, err)
	}
}
