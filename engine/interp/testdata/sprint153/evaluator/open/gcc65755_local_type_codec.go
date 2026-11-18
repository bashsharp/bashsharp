// Reduced from fixedbugs/gcc65755.go: two function-local types named s with
// different shapes each cross to reflect.TypeOf. The interpreter scopes the
// declarations (see local_type_shadow); the dependency bridge keys local
// struct codecs by bare name, so the second shape collides in the worker
// and the program exits 2 without a diagnostic.
package main

import (
	"fmt"
	"reflect"
)

type S1 struct{}

func (S1) Fix() string {
	type s struct {
		f int
	}
	return reflect.TypeOf(s{}).Field(0).Name
}

type S2 struct{}

func (S2) Fix() string {
	type s struct {
		g bool
	}
	return reflect.TypeOf(s{}).Field(0).Name
}

func main() {
	f1 := S1{}.Fix()
	fmt.Println(f1)
	f2 := S2{}.Fix()
	fmt.Println(f2)
}
