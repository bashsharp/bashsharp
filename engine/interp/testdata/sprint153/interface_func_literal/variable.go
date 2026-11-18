// Positive control: a closure bound to a variable first, then stored in an
// interface, was already supported.
package main

import "fmt"

func main() {
	double := func(n int) int { return n * 2 }
	var x interface{}
	x = double
	fmt.Println(x.(func(int) int)(21))
}
