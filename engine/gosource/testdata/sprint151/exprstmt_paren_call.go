// Mechanism: expression statement whose outer node is not a CallExpr or a
// receive. Go only admits calls and receives in statement position, so the
// remaining legal shape is a parenthesized call or receive (*ast.ParenExpr).
package main

import "fmt"

func hello() int {
	fmt.Println("hello")
	return 1
}

func main() {
	(hello())
	ch := make(chan int, 1)
	ch <- 2
	(<-ch)
	fmt.Println("done")
}
