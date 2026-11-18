package main

import "fmt"

type item struct {
	value int
}

func main() {
	var value any = &item{value: 42}
	fmt.Println(value.(*item).value)
}
