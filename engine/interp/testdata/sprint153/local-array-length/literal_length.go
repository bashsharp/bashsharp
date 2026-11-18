// Positive control: a literal-length local array type stays materialised and
// still crosses the dependency boundary.
package main

import "fmt"

type pair [2]int

func main() {
	p := pair{4, 5}
	fmt.Println("pair", p)
}
