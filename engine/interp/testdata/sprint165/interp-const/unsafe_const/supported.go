package main

import "fmt"

const (
	base  = 2
	total = base<<3 + len("go")
)

func main() { fmt.Println(total) }
