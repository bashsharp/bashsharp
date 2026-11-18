package main

import "fmt"

func read(values []int, index int) {
	defer func() {
		if recovered := recover(); recovered != nil {
			panic("in-range read panicked")
		}
	}()
	fmt.Println(values[index])
}

func main() {
	read([]int{7}, 0)
}
