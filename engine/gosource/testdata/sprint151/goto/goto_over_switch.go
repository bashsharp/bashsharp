// Mechanism: forward goto that skips a switch, and a goto from inside a
// switch arm and a select arm to labels outside them.
package main

import "fmt"

func classify(n int) string {
	if n < 0 {
		goto negative
	}
	switch {
	case n == 0:
		goto zero
	case n%2 == 0:
		return "even"
	}
	return "odd"
zero:
	return "zero"
negative:
	return "negative"
}

func drain(ch chan int) int {
	total := 0
loop:
	select {
	case v := <-ch:
		total += v
		goto loop
	default:
		goto done
	}
done:
	return total
}

func main() {
	for _, n := range []int{-3, 0, 2, 7} {
		fmt.Println(n, classify(n))
	}
	ch := make(chan int, 3)
	ch <- 1
	ch <- 2
	ch <- 3
	fmt.Println(drain(ch))
}
