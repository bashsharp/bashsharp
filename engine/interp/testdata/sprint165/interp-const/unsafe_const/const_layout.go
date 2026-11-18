package main

import (
	"fmt"
	"unsafe"
)

type layout struct {
	flag byte
	word int64
}

type embedded struct{ word int64 }
type promoted struct {
	flag byte
	embedded
}

func width() int64 { return 0 }

const (
	pointerBytes  = unsafe.Sizeof((*byte)(nil))
	functionBytes = unsafe.Sizeof(func() {})
	callBytes     = unsafe.Sizeof(width())
	realPart      = real(3 + 4i)
	stringLength  = len("abc")
)

func main() {
	var value layout
	const (
		localBytes = unsafe.Sizeof(func() {})
		repeatedBytes
	)
	fmt.Println(pointerBytes, functionBytes, callBytes, localBytes == repeatedBytes, realPart, stringLength)
	fmt.Println(unsafe.Sizeof(value), unsafe.Alignof(value), unsafe.Offsetof(value.word))
	var nested promoted
	fmt.Println(unsafe.Offsetof(nested.word))
}
