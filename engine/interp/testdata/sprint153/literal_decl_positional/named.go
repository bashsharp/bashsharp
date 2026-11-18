// Positive control: a short declaration naming a function value or a
// closure variable binds that function, and a literal in a function with
// scalar arguments is the literal.
package main

import "fmt"

func inc(n int) int { return n + 1 }

func e(n int) int {
	i := 1
	return i + n
}

func main() {
	f := inc
	g := f
	fmt.Println(g(1), e(0))
	double := func(n int) int { return n * 2 }
	h := double
	fmt.Println(h(4))
}
