// Reduced from typeparam/issue48645a.go — reflect.TypeOf on an instantiated generic closure type.
package main

import (
	"fmt"
	"reflect"
)

func Pipe[R any]() {
	it := func(fn func(R) bool) {}
	fmt.Println(reflect.TypeOf(it).String())
}

func main() {
	Pipe[int]()
}
