// A value of a named array type whose length is a constant name crossing into
// an imported call. The helper cannot materialise the name, so the value must
// travel under its realised structural spelling.
package main

import "fmt"

const w = 3

type row [w]int

func main() {
	r := row{1, 2, 3}
	fmt.Println(r)
	fmt.Println(len(r))
}
