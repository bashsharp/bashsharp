// Positive control: one-result calls in argument position, and multi-result
// calls bound by a declaration first, were already supported.
package main

import "fmt"

func add(a, b int) int { return a + b }
func one() int         { return 9 }
func pair() (int, int) { return 1, 2 }

func main() {
	fmt.Println(add(one(), one()))
	x, y := pair()
	fmt.Println(add(x, y))
	fmt.Println(add(add(one(), 1), 2))
}
