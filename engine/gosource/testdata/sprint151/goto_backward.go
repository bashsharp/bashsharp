// Mechanism: backward goto forming a loop. The LabeledStmt is visited first.
package main

import "fmt"

func main() {
	i := 0
again:
	i++
	if i < 5 {
		goto again
	}
	fmt.Println(i)
}
