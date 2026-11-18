// Reduced from typeparam/boundmethod.go: reflect.DeepEqual on two local
// slices. The bridge's slice policy refuses it ("native slice retention or
// mutation is unsupported for reflect.DeepEqual").
package main

import (
	"fmt"
	"reflect"
)

func main() {
	got := []string{"1", "2"}
	want := []string{"1", "2"}
	fmt.Println(reflect.DeepEqual(got, want))
}
