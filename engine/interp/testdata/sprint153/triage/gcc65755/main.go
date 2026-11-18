// Reduced from fixedbugs/gcc65755.go — two methods each define a local type `s`;
// their reflect type descriptors must not collide.
package main

import (
	"fmt"
	"reflect"
)

type S1 struct{}

func (S1) Fix() string {
	type s struct{ f int }
	return reflect.TypeOf(s{}).Field(0).Name
}

type S2 struct{}

func (S2) Fix() string {
	type s struct{ g bool }
	return reflect.TypeOf(s{}).Field(0).Name
}

func main() {
	f1 := S1{}.Fix()
	f2 := S2{}.Fix()
	fmt.Println(f1, f2)
	if f1 != "f" || f2 != "g" {
		panic(f1 + f2)
	}
}
