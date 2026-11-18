package main

import "fmt"

func allocate(length int) {
	defer func() {
		fmt.Println(recover())
	}()
	values := make([]int, length)
	fmt.Println(len(values))
}

func main() {
	allocate(-1)
}
