//go:build full

package interp_test

// Sprint: #374; Story: #1512; Story-ID: 5137f4b98e0c

import "testing"

// A positional struct literal stores each element under the field's declared
// type. A method value stored that way is a live closure, so comparing the
// field back against nil must answer rather than refuse: the zero-value
// pre-fill's layout entry for a func-typed field must not survive beside the
// positional value the way the keyed form already replaces it.
func TestS374PositionalLiteralFuncFieldNilCompare(t *testing.T) {
	const source = `package main

import "fmt"

type Qualifier func(*Package) string

type Package struct{ path string }

func (p *Package) Path() string { return p.path }

type Checker struct{}

func (c *Checker) qualifier(pkg *Package) string { return pkg.Path() }

type typeWriter struct {
	qf Qualifier
}

func packagePrefix(pkg *Package, qf Qualifier) string {
	if qf != nil {
		return qf(pkg)
	}
	return "nil:" + pkg.path
}

func main() {
	var check Checker
	w := &typeWriter{check.qualifier}
	fmt.Println(packagePrefix(&Package{path: "example.com/p"}, w.qf))
	fmt.Println(packagePrefix(&Package{path: "example.com/q"}, nil))
}
`
	got, err := runGoSourceIdentity(t, source, "")
	want := "example.com/p\nnil:example.com/q\n"
	if err != nil || got.stdout != want || got.stderr != "" || got.status != 0 {
		t.Fatalf("outcome=%+v; err=%v", got, err)
	}
}
