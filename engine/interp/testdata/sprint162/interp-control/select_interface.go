package main

import "fmt"

func main() {
	var value any
	ch := make(chan int, 1)
	ch <- 7
	select {
	case value = <-ch:
	}
	fmt.Printf("%T %v\n", value, value)
}
