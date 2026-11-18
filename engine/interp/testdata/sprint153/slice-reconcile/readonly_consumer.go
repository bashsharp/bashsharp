// A dependency call that only reads its transported slice copies: the
// elements come back unchanged and nothing is written back.
package main

import (
	"fmt"
	"reflect"
)

func main() {
	got := []int{1, 2, 3}
	want := []int{1, 2, 3}
	fmt.Println(reflect.DeepEqual(got, want))
	fmt.Println(reflect.DeepEqual(got, []int{1, 2}))
	fmt.Println(got)
}
