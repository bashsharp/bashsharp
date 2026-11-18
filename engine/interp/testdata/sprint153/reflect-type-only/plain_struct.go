package main

import (
	"fmt"
	"reflect"
)

type point struct{ X, Y int }

func main() {
	p := point{1, 2}
	t := reflect.TypeOf(p)
	fmt.Println(t.Name(), t.NumField())
	v := reflect.ValueOf(p)
	fmt.Println(v.Field(0).Int(), v.Field(1).Int())
}
