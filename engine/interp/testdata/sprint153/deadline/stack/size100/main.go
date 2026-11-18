package main

import "fmt"

type T [20]int

var t T

func recur(n int) int {
	s := 0
	for _, v := range t {
		s += v
	}
	if n == 0 {
		return s
	}
	return s + recur(n-1)
}
func main() {
	for i := range t {
		t[i] = 1
	}
	fmt.Println(recur(100))
}
