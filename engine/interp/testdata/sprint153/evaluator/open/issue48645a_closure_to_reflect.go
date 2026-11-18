// Reduced from typeparam/issue48645a.go: a closure handed to
// reflect.TypeOf. Function values do not cross to the dependency
// ("asynchronous or retained original function callbacks are unsupported").
package main

import (
	"fmt"
	"reflect"
)

func main() {
	it := func(fn func(int) bool) {}
	fmt.Println(reflect.TypeOf(it).String())
}
