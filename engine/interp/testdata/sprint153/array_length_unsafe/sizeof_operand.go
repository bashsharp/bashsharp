// unsafe.Sizeof and unsafe.Alignof are compile-time uintptr constants read from
// the operand's static type, so they are valid inside an array length. Evaluate
// them from the operand rather than handing the pseudo-function to a dependency
// (where it has no callable symbol). The operand's type is fixed by its own
// syntax here: a conversion to a basic type, and a call of a declared function
// with a basic result.
package main

import (
	"fmt"
	"unsafe"
)

func width() int64 { return 0 }

func main() {
	type Conv [unsafe.Sizeof(int32(0))]byte
	type Call [unsafe.Sizeof(width())]byte
	type Align [unsafe.Alignof(int16(0))]byte
	var c Conv
	var k Call
	var a Align
	fmt.Println(len(c), len(k), len(a))
}
