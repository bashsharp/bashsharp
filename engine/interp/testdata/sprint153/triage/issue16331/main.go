// Reduced from fixedbugs/issue16331.go — reflect.MakeFunc / method value via reflect.
package main

import (
	"fmt"
	"reflect"
)

func F(args []reflect.Value) (results []reflect.Value) { return nil }

func main() {
	t := reflect.TypeOf((func())(nil))
	f := reflect.MakeFunc(t, F).Interface().(func())
	f()
	fmt.Println("ok")
}
