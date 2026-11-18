// Mechanism: function value type — a package-qualified generic function
// referenced through a selector as a value. go/types spells its type with a
// type-parameter list, which parser.ParseExpr cannot parse.
package main

import (
	"fmt"
	"slices"
)

func main() {
	f := slices.Max[[]int]
	fmt.Println(f([]int{3, 9, 4}))
}
