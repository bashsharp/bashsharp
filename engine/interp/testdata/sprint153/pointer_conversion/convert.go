// A conversion to a pointer type, (*T)(p), names the same storage p names
// and reads it as a T: methods of *T resolve on it, a write through it is
// seen by the original variable, and it converts back and forth between
// pointer types with identical underlying element types.
package main

import "fmt"

type p int
type q int

func (x *p) v() int { return int(*x) }

func mk() (*p, int) {
	n := 7
	return (*p)(&n), n
}

func show(x *p) { fmt.Println("show", x.v()) }

func main() {
	n := 3
	ptr := (*p)(&n)
	fmt.Println(ptr.v())
	*ptr = 5
	fmt.Println(n)
	var qp *q = (*q)(ptr)
	fmt.Println(*qp)
	a, b := mk()
	fmt.Println(a.v(), b)
	var ip *int = (*int)(ptr)
	fmt.Println(*ip)
	show((*p)(&n))
	var np *int
	fmt.Println((*p)(np) == nil)
}
