//go:build full

package interp_test

import (
	"strings"
	"testing"
)

// A differently laid-out struct pointer can name the same allocation when
// both structs have the same size and alignment. Padding belongs to that
// allocation even though ordinary struct reads and equality ignore it.
func TestS374UnsafeStructOverlayPadding(t *testing.T) {
	differGoSource(t, `package main

import (
	"fmt"
	"unsafe"
)

type S struct {
	A int8
	B int16
}

type S2 struct {
	A       int8
	padding int8
	B       int16
}

func main() {
	x := S{A: 1, B: 3}
	y := S{A: 2, B: 4}
	px := (*S2)(unsafe.Pointer(&x))
	px.padding = 88
	(*S2)(unsafe.Pointer(&y)).padding = 99
	fmt.Println(x.A, x.B, x == S{A: 1, B: 3})
	fmt.Println(y.A, y.B, y == S{A: 2, B: 4})
	fmt.Println((*S2)(unsafe.Pointer(&x)).padding)
	fmt.Println((*S2)(unsafe.Pointer(&y)).padding)
	x.B = 7
	fmt.Println(px.A, px.padding, px.B)
}
	`, nil, "")

	_, stderr, err := runGoSource(t, "unsupported-overlay", `package main
import "unsafe"
type Small struct { X int32 }
type Large struct { X int64 }
func main() {
	x := Small{X: 1}
	println((*Large)(unsafe.Pointer(&x)).X)
}
`)
	if got := stderr + errorText(err); err == nil || !strings.Contains(got, "BASHPP-EUNSAFE-LAYOUT") || !strings.Contains(got, "cannot share a struct overlay") {
		t.Fatalf("unsupported overlay: err=%v stderr=%q; want clear layout refusal", err, stderr)
	}
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
