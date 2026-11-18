package main

import "fmt"

const size = 2

func f(s []int) { c := make(chan []int, 1); c <- []int{size}; defer copy(s, <-c) }
func main()     { x := make([]int, 1); f(x); fmt.Println(x[0]) }
