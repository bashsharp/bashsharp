package main

import (
	"fmt"
	"unsafe"
)

const x = unsafe.Sizeof([8]byte{})

const (
	first = iota + int(x)
	second
)

func main() {
	var b [x]int
	fmt.Println(len(b), first, second)
}
