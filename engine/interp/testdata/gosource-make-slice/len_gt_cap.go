package main

import (
	"fmt"
	"runtime"
)

func main() {
	defer func() {
		v := recover()
		_, e := v.(error)
		_, r := v.(runtime.Error)
		fmt.Printf("recover %t %t %v\n", e, r, v)
	}()
	n := 2
	_ = make([]int, n, 1)
	fmt.Println("after")
}
