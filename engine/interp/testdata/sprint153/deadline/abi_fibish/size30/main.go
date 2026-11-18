package main

import "fmt"

const size = 30

func f(x int) (int, int) {
	if x < 3 {
		return 0, x
	}
	a, b := f(x - 2)
	c, d := f(x - 1)
	return a + d, b + c
}
func main() { a, b := f(size); fmt.Printf("f(%d)=%d,%d\\n", size, a, b) }
