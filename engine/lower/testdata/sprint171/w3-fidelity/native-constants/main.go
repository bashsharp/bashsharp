package main

import "unsafe"

const (
	outer = iota
	_     = unsafe.Sizeof(func() {
		var first [iota]int
		var second [iota]int
		const (
			zero = iota
			one
			_ = unsafe.Sizeof([iota - 1]int{} == first)
			_ = unsafe.Sizeof([iota - 2]int{} == second)
		)
	})
	after = iota
)

func main() {
	if outer != 0 || after != 2 {
		panic("outer iota changed")
	}
}
