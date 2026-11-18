// The constant-expression form: [w * w]byte crossing by realised length.
package main

import "fmt"

const w = 2

type block [w * w]byte

func main() {
	var b block
	b[3] = 7
	fmt.Println(b)
}
