// Positive control: new(T) in a declaration and &T{} in an assignment were
// already supported.
package main

import "fmt"

type S struct{ n int }

func main() {
	q := new(int)
	*q = 4
	fmt.Println(*q)
	var s *S = new(S)
	fmt.Println(s.n)
	ptrs := make([]*S, 1)
	ptrs[0] = &S{3}
	fmt.Println(ptrs[0].n)
}
