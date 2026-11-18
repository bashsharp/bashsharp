// Mechanism: backward goto forming a loop, with the label on a short
// declaration that re-runs on every pass. Each pass declares a fresh variable,
// so a closure keeps the cell of the pass that created it.
package main

import "fmt"

func main() {
	i := 0
	sum := 0
	first := func() int { return -1 }
	last := first
again:
	step := i * 2
	sum += step
	if i == 0 {
		first = func() int { return step }
	}
	last = func() int { return step }
	i++
	if i < 5 {
		goto again
	}
	fmt.Println(i, sum, step)
	fmt.Println(first(), last())
}
