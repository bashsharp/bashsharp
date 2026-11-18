package main

import "fmt"

func main() {
	y := new([3]byte)
	for i := 0; i < 3; i++ {
		y[i] = 99
	}
	fmt.Println(y[1], len(y), *y)
	p := &[2]int{1, 2}
	p[0] = 7
	fmt.Println(p[0], p[1], p[:])
	defer func() {
		fmt.Println("recovered:", recover())
	}()
	var q *[2]int
	q[0] = 1
}
