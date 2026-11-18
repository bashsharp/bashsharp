//go:build full

package interp_test

import "testing"

// The comparison closure reads the same slice sort permutes. A dependency
// copy would make the comparison see stale elements after the first swap.
func TestS275ReflectMadeSortSliceStableSharesStorage(t *testing.T) {
	src := `package main
import (
	"reflect"
	"sort"
)
func main() {
	x := []int{3, 1, 2}
	less := reflect.MakeFunc(reflect.TypeOf((func(int, int) bool)(nil)), func(args []reflect.Value) []reflect.Value {
		i, j := int(args[0].Int()), int(args[1].Int())
		return []reflect.Value{reflect.ValueOf(x[i] < x[j])}
	}).Interface().(func(int, int) bool)
	sort.SliceStable(x, less)
	println(x[0], x[1], x[2])
}`
	_, stderr, err := runGoSource(t, "s275-reflect-made-sort-stable", src)
	if err != nil || stderr != "1 2 3\n" {
		t.Fatalf("shared stable sort: err=%v stderr=%q", err, stderr)
	}
}
