package main

import (
	"fmt"
	"reflect"
)

type S struct{ Data any }
type M struct{}

func (*M) Run([]byte) (S, error) { return S{Data: "hello"}, nil }

const size = 10

func one() any {
	f := reflect.ValueOf(&M{}).MethodByName("Run")
	s, _ := f.Interface().(func([]byte) (S, error))(nil)
	return s
}
func main() {
	var last any
	for i := 0; i < size; i++ {
		last = one()
	}
	fmt.Printf("%T\\n", last)
}
