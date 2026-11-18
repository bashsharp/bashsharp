// Mechanism: one label used both by a labeled break/continue and by a goto,
// a labeled switch broken from inside a nested loop, and a labeled break to
// the innermost statement (which must keep its label when lowered).
package main

import "fmt"

func main() {
	tries := 0
retry:
	tries++
outer:
	for i := 0; i < 3; i++ {
		for j := 0; j < 3; j++ {
			if j == 2 {
				continue outer
			}
			if i == 2 {
				break outer
			}
			fmt.Println(i, j)
		}
	}
	if tries < 2 {
		goto retry
	}
sw:
	switch tries {
	case 2:
		for k := 0; k < 5; k++ {
			if k == 1 {
				break sw
			}
			fmt.Println("k", k)
		}
		fmt.Println("unreached")
	}
	fmt.Println("tries", tries)
direct:
	switch tries {
	case 2:
		break direct
	default:
		fmt.Println("unreached")
	}
	fmt.Println("done")
}
