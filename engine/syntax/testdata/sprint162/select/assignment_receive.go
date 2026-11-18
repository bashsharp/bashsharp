package main

import "fmt"

func main() {
	ch := make(chan int, 2)
	ch <- 7
	ch <- 9
	var value int
	var open bool
	select {
	case value = <-ch:
		fmt.Println(value)
	}
	select {
	case _, open = <-ch:
		fmt.Println(open)
	}
}
