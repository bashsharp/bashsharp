//go:build full

package interp_test

// Sprint: #209; Story: #460; Story-ID: d8e7d58f362b

import (
	"testing"

	"github.com/go-quicktest/qt"
)

func TestStory460UnsafeSizeofLaterFunctionResult(t *testing.T) {
	src := `package main
import (
	"fmt"
	"unsafe"
)
type A [unsafe.Sizeof(byte(0))]*byte
func F() byte { panic("F must not run") }
type B [unsafe.Sizeof(F())]*byte
func main() {
	b := byte(1)
	fmt.Println(*A{&b}[0] + *B{&b}[0])
}`
	out, stderr, err := runGoSource(t, "story460sizeof", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "2\n"))
}
