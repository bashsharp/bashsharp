// A local generic type instantiated with concrete arguments and handed to
// fmt without any method: its fields cross by structure under the substituted
// element types.
package main

import "fmt"

type Pair[A any, B any] struct {
	Left  A
	Right B
}

func main() {
	p := Pair[string, int]{Left: "x", Right: 9}
	fmt.Println(p)
	fmt.Printf("%v %v\n", p.Left, p.Right)
}
