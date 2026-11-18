//go:build full

// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp_test

import (
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/go-quicktest/qt"
)

func TestS219CollectionValueStorageAndBuiltins(t *testing.T) {
	const src = `package main
import "fmt"
type F func(int) (int, int)
type R <-chan int
type V int
func (V) M() int { return 1 }
func pair(x int) (int, int) { return x, x }
func main() {
	fs := []F{pair}
	a, b := fs[0](7)
	ch := make(chan int, 1)
	var recv R = ch
	recvs := append([]R{}, recv)
	ch <- 9
	var values []any
	values = append(values, V(3))
	fmt.Println(a, b, <-recvs[0], values[0].(V).M())
}
`
	out, stderr, err := runGoSource(t, "s219values", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "7 7 9 1\n"))
}

func TestS219SliceCapacityCopyAndAlias(t *testing.T) {
	const src = `package main
import "fmt"
func main() {
	a := [8]byte{0, 1, 2, 3, 4, 5, 6, 7}
	b := a
	copy(a[2:7], a[0:5])
	x := make([]byte, 8, 16)
	for i := range x {
		x[i] = byte(i)
	}
	y := x
	n := copy(x[2:7], x[0:5])
	z := append(y[:3], byte(8), byte(9))
	fmt.Println(n, len(z), cap(z), x[2], x[6], y[3], y[4], a[2], b[2])
}
`
	out, stderr, err := runGoSource(t, "s219capacity", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "5 5 16 0 4 8 9 0 2\n"))
}

func TestS219GlobalArrayStorage(t *testing.T) {
	const src = `package main
import "fmt"
var data = [1024]byte{42}
func main() { fmt.Println(data[0], data[len(data)-1]) }
`
	out, stderr, err := runGoSource(t, "s219globalarray", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "42 0\n"))
}

func TestS219CollectionBuiltinNegativeGuards(t *testing.T) {
	for _, body := range []string{
		`s := []int{1}; _ = append(s, "bad")`,
		`dst := []int{1}; src := []string{"bad"}; _ = copy(dst, src)`,
	} {
		_, stderr, err := runBashSharpCall(t, "func main() { "+body+" }\nmain()\n")
		qt.Assert(t, qt.ErrorIs(err, interp.ExitStatus(2)))
		if !strings.HasPrefix(stderr, "BASHPP-EBUILTIN-TYPE:") {
			t.Fatalf("stderr %q does not contain builtin type diagnostic", stderr)
		}
	}
}
