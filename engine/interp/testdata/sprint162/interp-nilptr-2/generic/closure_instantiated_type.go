package main

import "fmt"

// A function literal made in an instantiated generic frame has the
// instantiated signature as its dynamic type: stored in an interface, it is
// asserted against the type written in the same frame.

func maker[T any]() func() *T {
	return func() *T { return new(T) }
}

func viaInterface[T any]() *T {
	var x interface{}
	x = maker[T]
	return x.(func() func() *T)()()
}

func describe[T any](v T) string {
	var x any = func() T { return v }
	if f, ok := x.(func() T); ok {
		return fmt.Sprint(f())
	}
	return "no match"
}

func main() {
	fmt.Println(*viaInterface[int](), *viaInterface[string]() == "")
	fmt.Println(describe(7), describe("seven"))
	var x any = func() int { return 1 }
	_, ok := x.(func() string)
	fmt.Println(ok)
}
