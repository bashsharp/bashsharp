//go:build full

package interp_test

import "testing"

// An unsafe.Pointer held in an interface and handed to the dependency —
// reflect.ValueOf(f) — crosses as the pointer it was converted from, labelled
// unsafe.Pointer. The worker refused that pairing ("pointer for
// unsafe.Pointer"); it now decodes the pointer at its pointee's type and
// converts, so reflect observes an unsafe.Pointer naming the same storage.
func TestS374UnsafePointerInterfaceCrossesToReflect(t *testing.T) {
	differGoSource(t, `package main

import (
	"fmt"
	"reflect"
	"unsafe"
)

type S struct {
	X unsafe.Pointer
	Y *byte
}

func main() {
	b := new(byte)
	var f, g any = unsafe.Pointer(b), unsafe.Pointer(b)
	v := reflect.ValueOf(f)
	fmt.Println(v.Kind(), v.IsNil(), reflect.TypeOf(f), fmt.Sprintf("%T", f))
	fmt.Println(v.Pointer() == reflect.ValueOf(g).Pointer())

	x := reflect.New(reflect.TypeOf(S{})).Elem()
	y := reflect.New(reflect.TypeOf(S{})).Elem()
	x.Field(0).Set(reflect.ValueOf(f))
	y.Field(0).Set(reflect.ValueOf(g))
	fmt.Println(x.Field(0).IsNil(), x.Interface() == y.Interface())

	var other any = unsafe.Pointer(new(byte))
	y.Field(0).Set(reflect.ValueOf(other))
	fmt.Println(x.Interface() == y.Interface())

	var p any = new(byte)
	x.Field(1).Set(reflect.ValueOf(p))
	fmt.Println(x.Field(1).IsNil(), x.Interface() == y.Interface())
}
`, nil, "")
}
