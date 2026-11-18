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
	s := make([]int, 2, 4)
	fmt.Println(len(s), cap(s))
	fmt.Println("after")
}
