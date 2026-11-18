// An explicit type argument spelled inside a generic body names that body's
// own type parameter: g[T]() inside a method of Foo[T] instantiates g at the
// receiver's T.
package main

import "fmt"

type Foo[T any] struct{}

func g[T any]() func() *T {
	return func() *T {
		return new(T)
	}
}

func zero[T any]() T {
	var z T
	return z
}

func (l *Foo[T]) f1() *T { return g[T]()() }
func (l *Foo[T]) f2() T  { return zero[T]() }

func pair[A, B any](a A, b B) string { return fmt.Sprintf("%v/%v", a, b) }

func describe[T, U any](t T, u U) string { return pair[U, T](u, t) }

func main() {
	foo := Foo[int]{}
	fmt.Println(*(foo.f1()), foo.f2())
	bar := Foo[string]{}
	fmt.Printf("%q %q\n", *(bar.f1()), bar.f2())
	fmt.Println(describe[int, string](1, "s"))
}
