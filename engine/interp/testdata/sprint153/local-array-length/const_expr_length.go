// The constant-expression form of the same shape: [w * w]byte.
package main

import "fmt"

const w = 4

type block [w * w]byte

func main() {
	var b block
	b[5] = 9
	fmt.Println("block", b[5], len(b))
}
