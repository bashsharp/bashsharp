package main

import "fmt"

type G[T any] struct{ V T }

type W struct {
	G[int]
	S string
}

func main() {
	w := W{G[int]{1}, "s"}
	fmt.Println(w)
}
