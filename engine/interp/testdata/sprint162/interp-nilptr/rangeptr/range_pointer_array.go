package main

import "fmt"

type T struct{ n int }

func count(p *[3]int) {
	// One iteration variable: len(*p) is constant, so p is never read.
	for i := range p {
		fmt.Println("index", i)
	}
}

func main() {
	arr := [3]int{10, 20, 30}
	p := &arr
	for i, v := range p {
		fmt.Println(i, v)
		arr[2] = 99 // ranging a pointer reads the elements in place
	}
	structs := [2]T{{1}, {2}}
	for i, t := range &structs {
		fmt.Println(i, t.n)
	}
	count(nil)
	defer func() {
		fmt.Println("recovered:", recover())
	}()
	var q *[3]int
	for i, v := range q {
		fmt.Println("unreachable", i, v)
	}
}

func init() {
	// The parenthesised explicit dereference is the same range: with one
	// iteration variable the pointer is not evaluated.
	var q *[2]int
	for i := range *q {
		fmt.Println("deref index", i)
	}
	r := &[2]int{4, 5}
	for i, v := range *r {
		fmt.Println("deref", i, v)
	}
}
