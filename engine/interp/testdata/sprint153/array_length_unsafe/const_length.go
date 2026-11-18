// Positive control: an array length given by an ordinary integer constant
// expression. The constant-folding path that unsafe.Sizeof now joins already
// handled this.
package main

import "fmt"

const n = 4

func main() {
	type T [n * 2]byte
	var t T
	fmt.Println(len(t))
}
