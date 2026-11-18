//go:build full

// Sprint: #219; Story: #463; Story-ID: a6f104b906d9
package interp_test

import (
	"testing"

	"github.com/go-quicktest/qt"
)

func TestS219ChannelAllocation(t *testing.T) {
	t.Run("negative capacity", func(t *testing.T) {
		src := `package main
import "runtime"
func main() {
	n := -1
	defer func() { p := recover(); e, isError := p.(error); _, isRuntime := p.(runtime.Error); println(isError, isRuntime, e.Error()) }()
	_ = make(chan int, n)
}`
		_, stderr, err := runGoSource(t, "s219channegative", src)
		qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
		qt.Assert(t, qt.Equals(stderr, "true true makechan: size out of range\n"))
	})

	t.Run("huge capacity within int", func(t *testing.T) {
		src := `package main
import "runtime"
func main() {
	n := int(^uint(0) >> 1)
	defer func() { p := recover(); e, isError := p.(error); _, isRuntime := p.(runtime.Error); println(isError, isRuntime, e.Error()) }()
	_ = make(chan int, n)
}`
		_, stderr, err := runGoSource(t, "s219chanhuge", src)
		qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
		qt.Assert(t, qt.Equals(stderr, "true true makechan: size out of range\n"))
	})

	t.Run("normal capacity", func(t *testing.T) {
		src := `package main
func main() { c := make(chan int, 2); c <- 7; println(len(c), cap(c), <-c) }`
		_, stderr, err := runGoSource(t, "s219channormal", src)
		qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
		qt.Assert(t, qt.Equals(stderr, "1 2 7\n"))
	})

	// This capacity fits int, but multiplying it by the larger element size
	// exceeds makechan's allocation bound without attempting an allocation.
	t.Run("element size overflow", func(t *testing.T) {
		src := `package main
import "runtime"
func main() {
	n := int(^uint(0) >> 1) / 8
	defer func() { p := recover(); e, isError := p.(error); _, isRuntime := p.(runtime.Error); println(isError, isRuntime, e.Error()) }()
	_ = make(chan complex128, n)
}`
		_, stderr, err := runGoSource(t, "s219chanelement", src)
		qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
		qt.Assert(t, qt.Equals(stderr, "true true makechan: size out of range\n"))
	})
}
