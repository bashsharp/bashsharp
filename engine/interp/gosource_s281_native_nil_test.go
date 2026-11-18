//go:build full

package interp_test

// Sprint: #281; Story: #809; Story-ID: fac7e14af4a8

import "testing"

func TestGoSourceS281NativeInterfaceNilUncomparableSlice(t *testing.T) {
	const source = `package main

import (
	"fmt"
	"reflect"
)

func main() {
	var typedNil any = reflect.ValueOf([][2]uint64(nil)).Interface()
	nonNilEmpty := reflect.ValueOf([][2]uint64{}).Interface()
	fmt.Println(typedNil == nil, nonNilEmpty == nil)
	fmt.Println(reflect.ValueOf(typedNil).IsNil(), reflect.ValueOf(nonNilEmpty).IsNil())
}
`
	differGoSource(t, source, nil, "")
}
