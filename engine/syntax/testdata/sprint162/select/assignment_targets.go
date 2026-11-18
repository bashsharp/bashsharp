package main

import "fmt"

type pair struct{ value int }

func main() {
	ch := make(chan int, 2)
	ch <- 3
	ch <- 5
	var p pair
	values := []int{0}
	select {
	case p.value = <-ch:
		fmt.Println(p.value)
	}
	select {
	case values[0] = <-ch:
		fmt.Println(values[0])
	}
}
