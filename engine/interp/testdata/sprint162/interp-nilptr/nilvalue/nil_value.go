package main

import (
	"fmt"
	"reflect"
)

type T struct{ n int }

func declared() {}

func classify(v interface{}) string {
	switch v {
	case nil:
		return "nil"
	case int(0):
		return "zero"
	default:
		return "other"
	}
}

func main() {
	f := func() {}
	fmt.Println(f == nil, f != nil, declared == nil)
	var g func()
	fmt.Println(g == nil)
	var m map[int]int
	var s []int
	fmt.Println(m == nil, s == nil)

	switch f {
	case nil:
		fmt.Println("BUG: func is nil")
	default:
		fmt.Println("func default")
	}
	switch mm := make(map[int]int); mm {
	case nil:
		fmt.Println("BUG: map is nil")
	default:
		fmt.Println("map default")
	}
	switch []int(nil) {
	case nil:
		fmt.Println("slice nil")
	default:
		fmt.Println("BUG")
	}
	switch (func() int)(nil) {
	case nil:
		fmt.Println("func nil")
	default:
		fmt.Println("BUG")
	}
	switch (map[int]int)(nil) {
	case nil:
		fmt.Println("map nil")
	default:
		fmt.Println("BUG")
	}
	var p *T
	switch p {
	case nil:
		fmt.Println("pointer nil")
	default:
		fmt.Println("BUG")
	}
	fmt.Println(classify(nil), classify(0), classify("x"))
	switch interface{}(nil) {
	case int(0):
		fmt.Println("BUG")
	default:
		fmt.Println("iface default")
	}

	fmt.Println(reflect.TypeOf(nil))
	fmt.Println(reflect.TypeOf((func())(nil)))
	fmt.Println(reflect.TypeOf((func(*int) bool)(nil)))
	fmt.Println(reflect.TypeOf((*T)(nil)))
	fmt.Println(reflect.TypeOf([]byte(nil)))
	fmt.Println(fmt.Sprint(nil), fmt.Sprintf("%v", (*int)(nil)))
}
