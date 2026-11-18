package main

import "fmt"

type number int

func (n *number) value() int {
	return int(*n)
}

func main() {
	n := number(42)
	f := n.value
	fmt.Println(n.value(), f())
}
