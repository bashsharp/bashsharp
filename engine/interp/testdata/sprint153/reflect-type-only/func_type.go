package main

import (
	"fmt"
	"reflect"
)

func f() int { return 0 }

func g() int { return 0 }

func h(a, b string) (bool, error) { return false, nil }

func typeof(x interface{}) string { return reflect.TypeOf(x).String() }

func main() {
	fmt.Println(typeof(f))
	fmt.Println(typeof(f) == typeof(g))
	fmt.Println(typeof(h))
}
