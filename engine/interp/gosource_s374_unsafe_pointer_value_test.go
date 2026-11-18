//go:build full

package interp_test

import "testing"

// A conversion to unsafe.Pointer read as a value — an interface's dynamic
// value, an element of an interface-typed collection, an argument for an
// interface parameter — is the pointer it retypes. It used to fall to the
// scalar evaluator, which has no reading of `new(T)` or `&x`, and two
// interfaces holding unsafe.Pointer values were refused as uncomparable.
func TestS374UnsafePointerConversionAsValue(t *testing.T) {
	differGoSource(t, `package main

import (
	"fmt"
	"unsafe"
)

func show(tag string, rows [][]any) {
	for _, row := range rows {
		fmt.Println(tag, len(row), row[0] != row[1], row[0] == row[0])
	}
}

func isPointer(v any) bool {
	_, ok := v.(unsafe.Pointer)
	return ok && v != nil
}

func main() {
	show("new", [][]any{
		{new(byte), new(byte)},
	})
	show("unsafe", [][]any{
		{unsafe.Pointer(new(byte)), unsafe.Pointer(new(byte))},
	})

	var x any = unsafe.Pointer(new(byte))
	_, ok := x.(unsafe.Pointer)
	fmt.Println(ok, x != nil, x == x)

	b := new(byte)
	same := []any{unsafe.Pointer(b), unsafe.Pointer(b)}
	fmt.Println(len(same), same[0] == same[1])

	var local byte
	addr := []any{unsafe.Pointer(&local), (unsafe.Pointer)(&local)}
	fmt.Println(addr[0] != nil, addr[0] == addr[1])

	fmt.Println(isPointer(unsafe.Pointer(new(int))), isPointer(new(int)))

	var null any = unsafe.Pointer(nil)
	fmt.Println(null != nil, null == unsafe.Pointer(nil))

	p := unsafe.Pointer(new(int))
	var boxed any = p
	fmt.Println(boxed == boxed, boxed == any(p))
}
`, nil, "")
}
