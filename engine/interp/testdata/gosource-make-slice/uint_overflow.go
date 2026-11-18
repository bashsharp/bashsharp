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
	n := uint64(1<<64 - 1)
	_ = make([]byte, n)
	fmt.Println("after")
}
