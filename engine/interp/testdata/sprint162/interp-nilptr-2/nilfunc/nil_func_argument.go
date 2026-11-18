package main

import "fmt"

// A func-typed parameter accepts a nil function value — the literal or a func
// variable holding no closure — and a call through it faults at call time as
// Go's nil function call does.

func h(p, q func() struct{}) bool {
	return p() == q()
}

func apply(f func(int) int, x int) int {
	if f == nil {
		return -x
	}
	return f(x)
}

func shouldPanic(name string, x func()) {
	defer func() {
		if recover() == nil {
			panic(name + " did not panic")
		}
		fmt.Println(name, "panicked")
	}()
	x()
}

func main() {
	shouldPanic("h", func() { h(nil, nil) })
	var g func() struct{}
	shouldPanic("h var", func() { h(g, g) })
	n := 0
	inc := func() struct{} {
		n++
		return struct{}{}
	}
	fmt.Println(h(inc, inc), n)
	fmt.Println(apply(nil, 3), apply(func(x int) int { return x * 2 }, 3))
	var double func(int) int
	fmt.Println(apply(double, 4))
	double = func(x int) int { return x + x }
	fmt.Println(apply(double, 4))
}
