package main

import "fmt"

type Empty struct{}
type Values []Empty

func main() {
	n, c := 2, 5
	s := make(Values, n, c)
	fmt.Println(len(s), cap(s), s == nil, len(s[:c]))
	z := make([][0]int, 0, c)
	fmt.Println(len(z), cap(z), z == nil)
	b := make([]byte, 0, c)
	fmt.Println(b[:c])
}
