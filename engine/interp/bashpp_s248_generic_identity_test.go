//go:build full

// Sprint: #248; Story: #700; Story-ID: 14e8b88629e0
package interp_test

import (
	"testing"

	"github.com/go-quicktest/qt"
)

// TestS248GenericNewDynamicType pins that `new(T)` inside a generic body
// allocates the frame's type argument. Boxed directly in a type switch or
// assertion operand — `interface{}(new(T)).(type)` — its dynamic type is *E,
// so interface cases answer from E's method set, including methods promoted
// through an embedded generic instantiation.
func TestS248GenericNewDynamicType(t *testing.T) {
	src := `package main
type E struct{}
func (E) EGood() {}
type X[T any] struct{ E }
func (X[T]) XGood() {}
type W struct{ X[int] }
func Test[T, Good any]() {
	switch interface{}(new(T)).(type) {
	case Good:
		print("ok ")
	default:
		print("miss ")
	}
	_, ok := any(new(T)).(Good)
	println(ok)
}
func Exact[T any]() {
	switch interface{}(new(T)).(type) {
	case *E:
		println("*E")
	case *W:
		println("*W")
	default:
		println("other")
	}
}
func main() {
	Test[E, interface{ EGood() }]()
	Test[X[int], interface{ EGood() }]()
	Test[X[int], interface{ XGood() }]()
	Test[W, interface{ EGood() }]()
	Test[W, interface{ XGood() }]()
	Test[int, interface{ EGood() }]()
	Exact[E]()
	Exact[W]()
	Exact[int]()
}`
	_, stderr, err := runGoSource(t, "s248-generic-new-dynamic", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr=%q", stderr))
	qt.Assert(t, qt.Equals(stderr, "ok true\nok true\nok true\nok true\nok true\nmiss false\n*E\n*W\nother\n"))
}
