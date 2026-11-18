// Positive control: method values and ordinary method calls on the same
// types were already supported.
package main

import "fmt"

type p int

func (x p) h(n int) p  { return x + p(n) }
func (x *p) inc(n int) { *x += p(n) }

func main() {
	v := p(10)
	fmt.Println(v.h(3))
	v.inc(5)
	fmt.Println(v)
	f := v.h
	fmt.Println(f(4))
	g := v.inc
	g(1)
	fmt.Println(v)
}
