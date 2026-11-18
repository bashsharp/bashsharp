// Mechanism: an indexed function callee whose index is an identifier. This
// used to be silently treated as a generic instantiation with type argument i.
package main

import "fmt"

func ten() int    { return 10 }
func twenty() int { return 20 }

func main() {
	steps := make([]func() int, 2)
	steps[0], steps[1] = ten, twenty
	i := 1
	fmt.Println(steps[i]())
}
