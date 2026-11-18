// Mechanism: a label on the empty statement at the end of a block, a label on
// a plain block, and the blank label `_:` — the one label Go allows to be
// unused (it cannot be the target of any goto).
package main

import "fmt"

func first(xs []int) int {
_:
	for _, x := range xs {
		if x > 10 {
			goto big
		}
	}
	return -1
big:
	return 10
}

func main() {
	n := 0
	{
		n++
		if n < 3 {
			goto end
		}
		n = 100
	end:
	}
	fmt.Println(n)
	goto block
block:
	{
		fmt.Println("block", n)
	}
_:
	fmt.Println(first([]int{1, 20}), first([]int{1, 2}))
}
