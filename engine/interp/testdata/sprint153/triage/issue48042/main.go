// Reduced from typeparam/issue48042.go — *T / new(T) inside a method of a generic type,
// where T is the enclosing method's type parameter (not directly instantiated here).
package main

import (
	"fmt"
	"reflect"
)

type Foo[T any] struct{}

func (l *Foo[T]) f() *T {
	return g[T]()()
}

func g[T any]() func() *T {
	return func() *T {
		t := new(T)
		reflect.ValueOf(t).Elem().SetInt(100)
		return t
	}
}

func main() {
	l := &Foo[int]{}
	fmt.Println(*l.f())
}
