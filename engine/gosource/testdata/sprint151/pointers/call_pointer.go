package main

import "fmt"

type box struct {
	value *int
}

func integer() *int {
	n := 41
	return &n
}

func boxed() *box {
	n := 42
	return &box{value: &n}
}

func main() {
	fmt.Println(*integer(), *boxed().value)
}
