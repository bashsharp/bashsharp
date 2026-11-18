//go:build full

// Sprint: #248; Story: #700; Story-ID: 14e8b88629e0
package interp_test

import (
	"testing"

	"github.com/go-quicktest/qt"
)

// TestS248PublicGenericIdentity pins a package-level generic type with
// methods, materialised in the dependency helper as the real generic
// declaration with generic method stubs. A struct embedding one of its
// instantiations crosses reflect.TypeOf under its own name, keeps the
// promoted field name, and presents the promoted method set natively; the
// instantiation reports its original spelling to fmt and reflect.
func TestS248PublicGenericIdentity(t *testing.T) {
	src := `package main
import (
	"fmt"
	"reflect"
)
type E struct{}
func (E) EGood() string { return "egood" }
type Box[T any] struct {
	E
	V T
}
func (b Box[T]) String() string { return fmt.Sprintf("box(%v)", b.V) }
func (b Box[T]) Get() T { return b.V }
func (b *Box[T]) Set(v T) { b.V = v }
type Pair[K comparable, V any] struct {
	K K
	V V
}
func (p Pair[K, V]) Key() K { return p.K }
// Receiver type parameters named like the helper's own protocol identifiers.
type Cell[value any] struct{ V value }
func (c Cell[value]) Get() value { return c.V }
func (c Cell[reflect]) String() string { return fmt.Sprint("cell ", c.V) }
type W struct {
	Box[int]
	P Pair[string, float64]
}
func TypeString[T any]() string { return reflect.TypeOf(new(T)).Elem().String() }
func main() {
	fmt.Println(TypeString[W](), TypeString[Box[int]](), reflect.TypeOf(Pair[string, float64]{}))
	fmt.Printf("%v %+v %T\n", W{Box[int]{V: 4}, Pair[string, float64]{"k", 1.5}}, W{}, W{})
	var x any = W{Box: Box[int]{V: 5}}
	v := reflect.ValueOf(x)
	fmt.Println(v.Type().Field(0).Name, v.Type().Field(0).Anonymous, reflect.TypeOf(W{}).NumMethod(), reflect.TypeOf(&W{}).NumMethod())
	fmt.Println(v.Field(0).Interface().(fmt.Stringer).String(), v.MethodByName("Get").Call(nil)[0].Int())
	fmt.Println(v.Interface().(interface{ EGood() string }).EGood())
	p := &Box[int]{}
	reflect.ValueOf(p).MethodByName("Set").Call([]reflect.Value{reflect.ValueOf(7)})
	fmt.Println(p.V, Box[string]{V: "s"})
	fmt.Println(Cell[int]{3}, reflect.ValueOf(Cell[string]{"x"}).MethodByName("Get").Call(nil)[0].String())
}`
	out, stderr, err := runGoSource(t, "s248-public-generic", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr=%q", stderr))
	qt.Assert(t, qt.Equals(out, "main.W main.Box[int] main.Pair[string,float64]\n"+
		"box(4) box(0) main.W\n"+
		"Box true 3 4\n"+
		"box(5) 5\n"+
		"egood\n"+
		"7 box(s)\n"+
		"cell 3 x\n"))
}

// TestS248PublicGenericWithdrawn pins the fallback: a generic whose
// constraint the helper cannot declare (a union interface) is withdrawn from
// the public set and its instantiations are materialised concretely, so its
// values still cross with their mirrored methods.
func TestS248PublicGenericWithdrawn(t *testing.T) {
	src := `package main
import "fmt"
type Num interface{ ~int | ~float64 }
type V[T Num] struct{ X T }
func (v V[T]) String() string { return fmt.Sprint("v=", v.X) }
func main() {
	fmt.Println(V[int]{3}, V[float64]{1.5})
}`
	out, stderr, err := runGoSource(t, "s248-public-generic-withdrawn", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr=%q", stderr))
	qt.Assert(t, qt.Equals(out, "v=3 v=1.5\n"))
}
