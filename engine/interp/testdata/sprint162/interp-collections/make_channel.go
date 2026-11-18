package main

import "fmt"

type numbers chan int

func main() {
	named := make(numbers, 1)
	named <- 7
	aggregate := make(chan [0]byte, 1)
	close(aggregate)
	fmt.Println(<-named)
}
