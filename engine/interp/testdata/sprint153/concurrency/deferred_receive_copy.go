package main

import "fmt"

func fill(dst []int) {
	c := make(chan []int, 1)
	c <- []int{1}
	defer copy(dst, <-c)
}

func main() {
	dst := make([]int, 1)
	fill(dst)
	fmt.Println(dst[0])
}
