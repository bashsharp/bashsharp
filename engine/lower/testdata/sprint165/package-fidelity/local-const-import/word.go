// Package fixture reproduces a function-local unsafe.Sizeof constant whose
// initializer is this file's only use of its import.
package fixture

import "unsafe"

func WordBytes() int {
	const wide = unsafe.Sizeof(uintptr(0)) == 8
	if wide {
		return 8
	}
	return 4
}
