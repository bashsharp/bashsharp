//go:build full

package interp_test

import "testing"

// Sprint: #275; Story: #758; Story-ID: 2577207b59b5

// Reused local generic names are distinct per declaration, and each
// instance keeps gc's public spelling: package-qualified, with type
// arguments separated by a bare comma.
func TestS275ScopedLocalGenericInstances(t *testing.T) {
	differGoSource(t, `package main
import (
 "fmt"
 "reflect"
)
func one() any { type T[_ any] int; return T[*[]map[string][2]float64](0) }
func two() any { type T[_ any] int; return T[*[]map[string][2]float64](0) }
func three() any { type T[_, _ any] int; return T[int, uint8](3) }
func main() {
 a, b, c := one(), two(), three()
 fmt.Println(reflect.TypeOf(a).String(), reflect.TypeOf(b).String(), reflect.TypeOf(c).String())
 fmt.Println(a == b, a == a)
}
`, nil, "")
}

// A type declared inside a generic function is a distinct type per
// instantiation of that function, and gc spells it with the enclosing type
// arguments and its package-wide local-type index (main.U[int;int]·3). The
// helper mirrors the declaring functions so gc builds exactly those types;
// earlier local declarations, including one in a closure, keep the index
// aligned while an alias takes none.
func TestS275NestedGenericLocalTypes(t *testing.T) {
	differGoSource(t, `package main
import (
 "fmt"
 "reflect"
)
type P[_ any] int
type Int int
func pad() int { type Q = int; type _ struct{}; f := func() int { type Z int; return int(Z(1)) }; return f() }
func F[A ~int]() [4]reflect.Type {
 type Int int
 type T[B ~int] struct{}
 type U[_ any] int
 type V U[int]
 return [4]reflect.Type{reflect.TypeOf(T[A]{}), reflect.TypeOf(U[A](0)), reflect.TypeOf(T[Int]{}), reflect.TypeOf(V(0))}
}
func main() {
 type Int int
 a, b, c := F[int](), F[Int](), F[int]()
 for _, ts := range [][4]reflect.Type{a, b} {
  fmt.Println(ts[0], ts[1], ts[2], ts[3])
 }
 fmt.Println(a == c, a == b, a[0] == c[0], a[1] == b[1])
 fmt.Println(reflect.TypeOf(P[int](0)), pad())
}
`, nil, "")
}
