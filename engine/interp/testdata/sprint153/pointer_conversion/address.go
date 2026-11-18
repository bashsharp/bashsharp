// Positive control: addresses, dereferences and nil pointer conversions
// without a retyping conversion were already supported.
package main

import "fmt"

type p int

func (x *p) v() int { return int(*x) }

func main() {
	var n p = 3
	ptr := &n
	fmt.Println(ptr.v())
	*ptr = 5
	fmt.Println(n)
	var np *p = (*p)(nil)
	fmt.Println(np == nil)
}
