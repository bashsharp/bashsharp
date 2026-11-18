package main

import "fmt"

type number complex128

func main() {
	values := []number{number(complex(3, 4)), 1 + 2i}
	if values[0] != number(3+4i) || values[1] != number(1+2i) {
		panic("complex collection changed")
	}
	fmt.Println("ok")
}
