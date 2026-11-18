//go:build full

package interp_test

import (
	"runtime"
	"testing"
)

// Sprint: #243; Story: #467; Story-ID: 62b1dab50f4a
func TestS243DefinedArrayPointerStorageThreeModes(t *testing.T) {
	typedSendThreeModes(t, `package main
import "fmt"
func main() {
 type Slice []int
 type P *[4]int
 s := make(Slice, 4)
 if p := P(s); &p[0] != &s[0] { panic("lost conversion storage") } else { p[1] = 7 }
 p := P(s)
 q := p
 q[2] = 9
 fmt.Println(s[1], s[2], &q[0] == &s[0])
 type Zero *[0]int
 var nilSlice Slice
 nilPointer := Zero(nilSlice)
 empty := make(Slice, 0)
 nonnilPointer := Zero(empty)
 fmt.Println(nilPointer == nil, nonnilPointer != nil)
}
`)
}

// Sprint: #243; Story: #426; Story-ID: 27f3e89e5692
func TestS243DereferencedAggregateInterfaceThreeModes(t *testing.T) {
	if runtime.GOARCH != "amd64" {
		t.Skip("unsafe blank views explicitly support amd64")
	}
	typedSendThreeModes(t, `package main
import ("fmt"; "unsafe")
type Source struct { x, y, z int }
type Blank struct { _ struct { a, b, c int } }
type Visible struct { N int }
func main() {
 var a, b any
 a = *(*Blank)(unsafe.Pointer(&Source{1, 2, 3}))
 b = *(*Blank)(unsafe.Pointer(&Source{4, 5, 6}))
 fmt.Println(a == b)
 if _, ok := a.(Blank); !ok { panic("lost blank dynamic type") }
 p := &Visible{7}
 var v any
 v = *p
 p.N = 9
 fmt.Println(v.(Visible).N, p.N)
}
`)
}
