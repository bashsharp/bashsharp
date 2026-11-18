package main

import (
	"fmt"
	"runtime"
)

func main() {
	defer func() { v := recover(); _, e := v.(error); _, r := v.(runtime.Error); fmt.Println(e, r, v) }()
	n := 1 << 20
	_ = make([][1 << 30]byte, n)
	panic("continued")
}
