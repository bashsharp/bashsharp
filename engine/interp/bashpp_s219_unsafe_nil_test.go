//go:build full

package interp_test

import (
	"testing"

	"github.com/go-quicktest/qt"
)

// TestS219UnsafeNil covers only unsafe.Pointer's typed nil value. Native
// storage reinterpretation remains outside the interpreter memory model.
func TestS219UnsafeNil(t *testing.T) {
	t.Run("typed identity", func(t *testing.T) {
		src := `package main
import (
	"reflect"
	"time"
	"unsafe"
)
func main() {
	var p unsafe.Pointer
	var v any = p
	println(p)
	println(p == nil, v != nil)
	t := reflect.TypeOf(v)
	println(t.PkgPath(), t.Name())
	u := reflect.TypeOf(time.Duration(0))
	println(u.PkgPath(), u.Name())
	q := reflect.TypeOf((*int)(nil))
	println(q.PkgPath() == "", q.Name() == "")
}`
		out, stderr, err := runGoSource(t, "s219-unsafe-nil", src)
		qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
		qt.Assert(t, qt.Equals(out, ""))
		qt.Assert(t, qt.Equals(stderr, "0x0\ntrue true\nunsafe Pointer\ntime Duration\ntrue true\n"))
	})

	// Sprint 374: a conversion to unsafe.Pointer is read as the pointer it
	// retypes, so discarding one is valid, as it is in Go.
	t.Run("non-nil conversion can be discarded", func(t *testing.T) {
		src := `package main
import "unsafe"
func main() {
	x := 1
	_ = unsafe.Pointer(&x)
}`
		_, stderr, err := runGoSource(t, "s219-unsafe-nonnil", src)
		qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	})
}
