// A function literal, or a generic function value (which the front end
// lowers to one), stored in an interface: the interface owns the closure and
// an assertion to its function type calls it.
package main

import "fmt"

func g[T any]() func() *T {
	return func() *T {
		t := new(T)
		return t
	}
}

func f5[T any]() *T {
	var x interface{}
	x = g[T]
	return x.(func() func() *T)()()
}

func main() {
	var x interface{}
	x = func(n int) int { return n * 2 }
	fmt.Println(x.(func(int) int)(21))
	var y any = func() string { return "y" }
	if f, ok := y.(func() string); ok {
		fmt.Println(f())
	}
	fmt.Println(*f5[int]())
}
