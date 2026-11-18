package main

import "fmt"

func main() {
	x := []int{1, 2, 3}
	i := 0
	i, x[i] = 1, 100
	if x[0] != 100 {
		fmt.Printf("%v, want 100,2,3\n", x)
		panic("failed")
	}
}
