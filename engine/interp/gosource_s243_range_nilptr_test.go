//go:build full

package interp_test

// Sprint: #243; Story: #674; Story-ID: 63073886bfce
import (
	"testing"
)

// TestS243RangeNilPointerArray pins the range semantics behind
// testdir:fixedbugs/bug454.go: ranging a nil pointer to a fixed-length array
// with the value variable blank (or absent) never reads an element, so the
// loop still walks the array's full index length; a non-blank value variable
// demands the elements and keeps the nil dereference fault. The toolchain
// oracle run is the control for both directions.
func TestS243RangeNilPointerArray(t *testing.T) {
	typedSendThreeModes(t, `package main
func main() {
	var arr *[10]int
	s := 0
	for i, _ := range arr {
		s += i
	}
	if s != 45 {
		panic("BUG")
	}
	t := 0
	for i := range arr {
		t += i
	}
	if t != 45 {
		panic("BUG2")
	}
	var failure any
	func() {
		defer func() { failure = recover() }()
		for _, v := range arr {
			_ = v
			break
		}
	}()
	if failure == nil {
		panic("no fault")
	}
	println("ok")
}`)
}
