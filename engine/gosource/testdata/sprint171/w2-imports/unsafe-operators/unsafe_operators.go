// Outside-corpus reproducer: unsafe.Sizeof, Alignof and Offsetof are
// constant operators, not calls — in a short declaration, an assignment, a
// return, a tuple, a var declaration, a call argument and a condition — and
// over a type parameter, where the instantiated frame settles the value.
package main

import (
	"fmt"
	"unsafe"
)

type pair struct {
	a int32
	b int64
}

func size[T any](x T) uintptr {
	return unsafe.Sizeof(x)
}

func report(n uintptr) string { return fmt.Sprint("size ", n) }

func sizes() (uintptr, uintptr) {
	var i8 int8
	var i64 int64
	return unsafe.Sizeof(i8), unsafe.Alignof(i64)
}

var packed = unsafe.Sizeof(pair{})

func main() {
	n := unsafe.Sizeof(0)
	fmt.Println(n)
	n = unsafe.Alignof("")
	fmt.Println(n)
	var p pair
	off := unsafe.Offsetof(p.b)
	fmt.Println(off, packed)
	a, b := sizes()
	fmt.Println(a, b)
	fmt.Println(report(unsafe.Sizeof(p)))
	if unsafe.Sizeof(p) != 16 {
		fmt.Println("unexpected layout")
	}
	var i interface{} = 0
	fmt.Println(unsafe.Sizeof(i) == 2*unsafe.Sizeof((*int)(nil)))
	fmt.Println(size(int32(0)), size("s"), size(p))
}
