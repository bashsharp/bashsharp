package main

import (
	"fmt"
	"runtime"
)

var order string

func length() int   { order += "l"; return -1 }
func capacity() int { order += "c"; return -1 }
func indirect() any { return recover() }
func main() {
	defer func() {
		if indirect() != nil {
			panic("indirect recover")
		}
		v := recover()
		_, e := v.(error)
		_, r := v.(runtime.Error)
		fmt.Println(order, e, r, v)
	}()
	_ = make([]byte, length(), capacity())
	panic("continued")
}
