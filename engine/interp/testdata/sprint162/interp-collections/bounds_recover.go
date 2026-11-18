package main

import "fmt"

func read(values []int, index int) {
	defer func() {
		fmt.Println(recover())
	}()
	fmt.Println(values[index])
}

func main() {
	read([]int{4, 5}, 2)
}
