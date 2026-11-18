package main

import (
	"fmt"
	"runtime"
)

func main() {
	defer func() { v := recover(); _, e := v.(error); _, r := v.(runtime.Error); fmt.Println(e, r, v) }()
	n, c := int64(1<<59), 1
	_ = make([]int, n, c)
	panic("continued")
}
