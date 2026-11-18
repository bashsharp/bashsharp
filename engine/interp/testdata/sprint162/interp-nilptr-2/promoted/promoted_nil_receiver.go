package main

import "fmt"

// A pointer-receiver method promoted from an embedded value field, selected
// through a nil pointer to the outer struct, faults with Go's nil dereference
// in every expression position; through a non-nil pointer it runs.

type Inner struct{ Err int }

func (i *Inner) M() int {
	if i == nil {
		return 86
	}
	return 17 + i.Err
}

type Outer struct{ Inner }

func shouldPanic(name string, f func()) {
	defer func() {
		fmt.Println(name, "recovered:", recover() != nil)
	}()
	f()
}

func main() {
	var o *Outer
	shouldPanic("println", func() { println(o.M()) })
	shouldPanic("fmt", func() { fmt.Println(o.M()) })
	shouldPanic("assign", func() { x := o.M(); fmt.Println(x) })
	shouldPanic("arith", func() { fmt.Println(o.M() + 1) })
	var i *Inner
	fmt.Println(i.M())
	fmt.Println((&Outer{Inner{Err: 3}}).M())
}
