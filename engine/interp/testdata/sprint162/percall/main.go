package main

import (
	"fmt"
	"os"
	"strconv"
)

func fib(n int) int {
	if n < 2 {
		return n
	}
	return fib(n-1) + fib(n-2)
}

func main() {
	n := 25
	if len(os.Args) == 2 {
		parsed, err := strconv.Atoi(os.Args[1])
		if err != nil || parsed < 25 || parsed > 30 {
			panic("n must be an integer from 25 through 30")
		}
		n = parsed
	}
	fmt.Printf("fib(%d)=%d\n", n, fib(n))
}
