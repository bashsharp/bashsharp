//go:build full

package interp_test

import (
	"strings"
	"testing"
)

// Sprint: #270; Story: #759; Story-ID: 9fa63957d084

// A typed-pointer conversion of an unsafe.Pointer to a layout the
// interpreter cannot view is legal Go; the pointer is stored, compared and
// converted back (fixedbugs/issue8004.go), and only an access through it is
// refused.
func TestS270G4UnsafeViewRefusedOnAccessOnly(t *testing.T) {
	src := `package main

import (
	"reflect"
	"unsafe"
)

type T struct {
	H *reflect.SliceHeader
	P *[]int
}

func main() {
	var all []T
	for i := 0; i < 3; i++ {
		p := new([]int)
		*p = append(*p, 1, 2, 3, 4)
		h := (*reflect.SliceHeader)(unsafe.Pointer(p))
		all = append(all, T{H: h}, T{P: p})
		back := (*[]int)(unsafe.Pointer(h))
		if back != p || unsafe.Pointer(h) != unsafe.Pointer(p) || (*back)[3] != 4 {
			panic("view lost its storage")
		}
	}
	println(len(all), (*all[1].P)[0])
	h := all[0].H
	println(h.Len)
}`
	out, stderr, err := runGoSource(t, "s270-g4-unsafe-view", src)
	if err == nil || out != "" || !strings.HasPrefix(stderr, "6 1\n") || !strings.Contains(err.Error()+stderr, "BASHPP-EUNSAFE-") {
		t.Fatalf("err=%v out=%q stderr=%q", err, out, stderr)
	}
}
