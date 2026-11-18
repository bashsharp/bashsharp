//go:build full

package interp_test

import (
	"testing"

	"github.com/go-quicktest/qt"
)

func TestS219NativeArrayKey(t *testing.T) {
	src := `package main
import (
	"fmt"
	"reflect"
)
type named [4]int32
func array(first int64) (reflect.Value, any) {
	t := reflect.ArrayOf(4, reflect.TypeOf(int32(0)))
	v := reflect.New(t).Elem()
	v.Index(0).SetInt(first)
	return v, v.Interface()
}
func slicePanics(m map[any]int) (got any) {
	defer func() { got = recover() }()
	m[[]int{1}] = 9
	return nil
}
func main() {
	v, k := array(1)
	m := map[any]int{}
	m[k] = 1
	_, equal := array(1)
	m[equal] = 2
	_, different := array(2)
	m[different] = 3
	v.Index(0).SetInt(7)
	_, original := array(1)
	_, mutated := array(7)
	fmt.Println(len(m), m[original], m[mutated])
	_, unnamed := array(0)
	m[named{}] = 4
	m[unnamed] = 5
	fmt.Println(len(m), m[named{}], m[unnamed])
	fmt.Println(slicePanics(m))
}`
	out, stderr, err := runGoSource(t, "s219nativearraykey", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "2 2 0\n4 4 5\nruntime error: hash of unhashable type []int\n"))
}
