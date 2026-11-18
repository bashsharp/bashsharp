package main

import (
	"fmt"
	"reflect"
)

type C struct{ n int }

func (c C) String() string { return "c!" }

func main() {
	c := C{1}
	t := reflect.TypeOf(c)
	fmt.Println(t.Name(), t.NumField(), t.Kind())
	fmt.Println(c)
}
