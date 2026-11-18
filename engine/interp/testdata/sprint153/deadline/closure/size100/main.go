package main

import "fmt"

const size = 100

func accum(n int) func(int) int { return func(i int) int { n += i; return n } }
func main() {
	a := accum(0)
	b := accum(1)
	total := 0
	for i := 0; i < size; i++ {
		total += a(2) + b(3) + a(4) + b(5)
	}
	fmt.Println(total)
}
