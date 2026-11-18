// Outside-corpus reproducer (issue60601's shape): unsafe.Sizeof over the
// dereference of new(T) under a type parameter. The static type walk had no
// pointee for `*new(T)` and dereferenced a nil pointer type; it must refuse
// honestly (or answer) rather than crash the interpreter.
package main

import (
	"fmt"
	"unsafe"
)

func size[T any]() uintptr {
	return unsafe.Sizeof(*new(T))
}

func main() {
	fmt.Println(size[int64]())
}
