// Mechanism: function value with a named func type (the mechanism the task
// lists explicitly). Recorded to show whether the named-type path survives.
package main

import "fmt"

type Handler func(int) int

func double(x int) int { return x * 2 }

func main() {
	var h Handler = double
	g := h
	fmt.Println(g(21))
}
