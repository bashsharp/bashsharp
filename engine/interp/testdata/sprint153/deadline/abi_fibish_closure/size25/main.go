package main

import "fmt"

type recur func(int, recur) (int, int)

const size = 25

func f(x int, self recur) (int, int) {
	if x < 3 {
		return 0, x
	}
	a, b := self(x-2, self)
	c, d := self(x-1, self)
	return a + d, b + c
}
func main() { a, b := f(size, f); fmt.Printf("f(%d)=%d,%d\\n", size, a, b) }
