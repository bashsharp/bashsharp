package main

import "fmt"

const (
	left  = 1
	right = 2
)

var p1, p2, p3 int

type (
	named int
	alias = named
)

func main() {
	p1, p2, p3 = left, right, int(alias(3))
	fmt.Println(p1, p2, p3)
}
