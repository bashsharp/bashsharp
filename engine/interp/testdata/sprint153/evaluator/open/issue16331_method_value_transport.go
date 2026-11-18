// Reduced from fixedbugs/issue16331.go: reflect.ValueOf(T{}).Method(0) on an
// interpreter-owned type, and (in the root) reflect.MakeFunc over an
// original function and a typed nil func value as a native argument. None
// of these cross the dependency bridge.
package main

import (
	"fmt"
	"reflect"
)

type T struct{}

func (T) M() { fmt.Println("M") }

func main() {
	f := reflect.ValueOf(T{}).Method(0).Interface().(func())
	f()
}
