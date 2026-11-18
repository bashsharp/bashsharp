// Reduced from fixedbugs/issue24491a.go — unsafe.Pointer <-> uintptr round-trip.
package main

import (
	"fmt"
	"unsafe"
)

func main() {
	s := "ok"
	p := uintptr(unsafe.Pointer(&s))
	if *(*string)(unsafe.Pointer(p)) != "ok" {
		panic("failed")
	}
	fmt.Println("ok")
}
