package main

import "fmt"

var value int

func pointer() *int {
	return &value
}

func main() {
	*pointer() = 42
	fmt.Println(value)
}
