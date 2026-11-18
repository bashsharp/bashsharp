package main

import "fmt"

type Pair struct {
	Base  int
	Extra string
}

func main() {
	fmt.Println(Pair{7, "x"})
}
