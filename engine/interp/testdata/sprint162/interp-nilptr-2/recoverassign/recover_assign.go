package main

import "fmt"

// The recovered value is assigned — to a variable, or to the blank
// identifier — as it is bound by a short declaration.

func discard() {
	_ = recover()
	fmt.Println("discarded")
}

func keep() {
	var r any
	r = recover()
	fmt.Println("kept:", r)
}

func typed() {
	var v interface{}
	v = recover()
	fmt.Println("fault:", v)
}

func quiet() {
	var r any
	r = recover()
	fmt.Println("nothing:", r == nil)
}

func main() {
	func() {
		defer discard()
		panic("first")
	}()
	func() {
		defer keep()
		panic("second")
	}()
	func() {
		defer typed()
		var p *int
		fmt.Println(*p)
	}()
	quiet()
}
