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
	n := -1
	s := []int{1}
	_ = append(s, make([]int, n)...)
	fmt.Println("after")
}
