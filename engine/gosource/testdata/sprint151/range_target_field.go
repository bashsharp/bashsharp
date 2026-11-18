// Mechanism: range assignment target that is a struct field (s.f).
package main

import "fmt"

type acc struct{ last string }

func main() {
	var s acc
	for _, s.last = range []string{"x", "y", "z"} {
	}
	fmt.Println(s.last)
}
