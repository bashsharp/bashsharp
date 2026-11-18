//go:build full

package interp_test

// Sprint: #319; Story: #1084; Story-ID: 7decf01bdd39

import "testing"

// cmd/compile/internal/ssa's slice allocators combine the uintptr constants
// produced by unsafe.Sizeof with a runtime uintptr conversion. Exercise the
// expression in the typed unsafe.Pointer field where the compiler package
// failed. The earlier uintptr arithmetic is retained to prevent a narrower
// address-only test from losing the source path that exposed the bug.
func TestGoSourceS319UnsafeSizeofRuntimeUintptrArithmetic(t *testing.T) {
	out, stderr, err := runGoSource(t, "s319-unsafe-sizeof-uintptr-arithmetic", `package main
import (
	"fmt"
	"unsafe"
)
type Cache struct{}
type Slice struct {
	Data unsafe.Pointer
	Len int
	Cap int
}
func (c *Cache) allocLimitSlice(n int) []limit {
	return make([]limit, n)
}
func (c *Cache) allocIDSlice(n int) int {
	var base limit
	var derived ID
	if unsafe.Sizeof(base)%unsafe.Sizeof(derived) != 0 {
		panic("bad")
	}
	scale := unsafe.Sizeof(base) / unsafe.Sizeof(derived)
	b := c.allocLimitSlice(int((uintptr(n) + scale - 1) / scale))
	s := Slice{
		Data: unsafe.Pointer(&b[0]),
		Len: n,
		Cap: cap(b) * int(scale),
	}
	if s.Data != unsafe.Pointer(&b[0]) {
		panic("pointer identity")
	}
	return s.Len
}
type Func struct{ values int }
func (f *Func) NumValues() int { return f.values }
func Run() int {
	c := new(Cache)
	f := &Func{values: 3}
	return c.allocIDSlice(f.NumValues())
}
type limit struct {
	min, max int64
	umin, umax uint64
}
type ID int32
func main() { fmt.Println(Run()) }
`)
	if err != nil || out != "3\n" {
		t.Fatalf("err=%v stdout=%q stderr=%q", err, out, stderr)
	}
}

// Every Go expression that produces unsafe.Pointer uses the same typed-value
// route when stored in an aggregate: live storage, nil, forged integer words,
// and unsafe.Add must not split into unrelated scalar-only special cases.
func TestGoSourceS319UnsafePointerAggregateValues(t *testing.T) {
	out, stderr, err := runGoSource(t, "s319-unsafe-pointer-aggregate-values", `package main
import (
	"fmt"
	"unsafe"
)
type Box struct{ P unsafe.Pointer }
func main() {
	var x int
	var bytes [2]byte
	live := Box{P: unsafe.Pointer(&x)}
	nilp := Box{P: nil}
	forged := Box{P: unsafe.Pointer(uintptr(0x1234))}
	moved := Box{P: unsafe.Add(unsafe.Pointer(&bytes[0]), 1)}
	fmt.Println(
		live.P == unsafe.Pointer(&x),
		nilp.P == nil,
		forged.P == unsafe.Pointer(uintptr(0x1234)),
		moved.P == unsafe.Pointer(&bytes[1]),
	)
}
`)
	if err != nil || out != "true true true true\n" {
		t.Fatalf("err=%v stdout=%q stderr=%q", err, out, stderr)
	}
}
