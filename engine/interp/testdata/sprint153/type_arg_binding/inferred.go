// Positive control: a generic function called from a generic body with its
// type arguments inferred, and a generic function value bound first, were
// already supported.
package main

import "fmt"

type Foo[T any] struct{}

func g[T any]() func() *T {
	return func() *T {
		return new(T)
	}
}

func id[T any](v T) T { return v }

func (l *Foo[T]) f2() *T {
	var f = g[T]
	return f()()
}

func (l *Foo[T]) f3(v T) T { return id(v) }

func main() {
	foo := Foo[int]{}
	fmt.Println(*(foo.f2()), foo.f3(5))
}
