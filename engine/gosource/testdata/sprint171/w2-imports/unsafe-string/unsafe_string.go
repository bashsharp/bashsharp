// Outside-corpus reproducer: unsafe.String over the program's own byte
// storage — an array element, a later element, a slice element, a single
// byte variable, a nil pointer with a zero length — and the run-time faults
// a nil pointer with a length and a negative length raise. A local type's
// own String method is the control the operator must not claim.
package main

import (
	"fmt"
	"unsafe"
)

type name string

func (n name) String() string { return "name:" + string(n) }

func check(label string, f func()) {
	defer func() { fmt.Println(label, recover()) }()
	f()
}

func main() {
	hello := [5]byte{'m', 'o', 's', 'h', 'i'}
	fmt.Println(unsafe.String(&hello[0], uint64(len(hello))))
	fmt.Println(unsafe.String(&hello[2], 2))
	s := []byte("world")
	fmt.Println(unsafe.String(&s[1], len(s)-1))
	var b byte = 'x'
	fmt.Println(unsafe.String(&b, 1), unsafe.String(&b, 0) == "")
	var p *byte
	fmt.Println(unsafe.String(p, 0) == "")
	fmt.Println(name("n").String())
	check("nil:", func() { fmt.Println(unsafe.String(p, 1)) })
	minus := -1
	check("negative:", func() { fmt.Println(unsafe.String(&b, minus)) })
}
