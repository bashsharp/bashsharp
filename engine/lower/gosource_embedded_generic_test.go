package lower_test

import (
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/lower"
)

func TestGoSourceEmbeddedGenericSelector(t *testing.T) {
	const source = `package main

type A1[T any] struct {
	value T
}

type A2[T any] interface {
	m(T)
}

type B1[T any] struct {
	*A1[T]
	A2[T]
}

type C[T any] struct {
	B1[T]
}

type D[T any] struct {
	C[T]
}

type impl[T any] struct{}

func (*impl[T]) m(T) {}

func set[T any](d *D[T], value T) {
	d.C.B1.A1 = &A1[T]{value: value}
	d.C.B1.A2 = &impl[T]{}
}

func main() {
	var d D[int]
	set(&d, 42)
}
`
	program, err := gosource.Parse(strings.NewReader(source), "embedded_generic.go", gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lower.Compile(program.File, lower.Options{Origin: "embedded_generic.go", Package: program.Package}); err != nil {
		t.Fatal(err)
	}
}
