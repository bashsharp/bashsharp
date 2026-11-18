//go:build full

// Sprint: #209; Story: #461; Story-ID: 4ed649697945
package interp_test

import (
	"strings"
	"testing"

	"github.com/go-quicktest/qt"
)

func TestStory461TupleAssignFromGoSourceFuncValues(t *testing.T) {
	cases := map[string]struct {
		source string
		want   string
	}{
		"func_typed_field": {`package main
import "fmt"
type C struct {
	a int
	x func(*C) int
}
func g(c *C) int { return c.a }
func main() {
	c := &C{a: 6}
	c.x = g
	var v int
	v = c.x(c)
	fmt.Println(v)
}`, "6\n"},
		"two_result_local_function": {`package main
import "fmt"
func pair(n int) (int, int) { return n, n + 1 }
func main() {
	var a, b int
	a, b = pair(4)
	fmt.Println(a, b)
}`, "4 5\n"},
		"generic_helper_tuple_call": {`package main
import "fmt"
type Complex interface{ ~complex64 | ~complex128 }
type complexAbs[T Complex] struct{ Value_ T }
func realimag(x any) (re, im float64) {
	switch z := x.(type) {
	case complex64:
		re = float64(real(z))
		im = float64(imag(z))
	case complex128:
		re = real(z)
		im = imag(z)
	}
	return
}
func (a complexAbs[T]) Abs() T {
	r, i := realimag(a.Value_)
	return T(complex(r+i, 0))
}
func main() {
	fmt.Println(complexAbs[complex128]{Value_: 2+3i}.Abs())
}`, "(5+0i)\n"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			out, stderr, err := runGoSource(t, "story461tuple"+name, tc.source)
			qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
			qt.Assert(t, qt.Equals(stderr, ""))
			qt.Assert(t, qt.Equals(out, tc.want))
		})
	}
}

func TestStory461ClassicTupleAssignStillRequiresDeclaredFunction(t *testing.T) {
	src := "func main() {\n a := 0\n b := 0\n a, b = missing()\n}\nmain()\n"
	_, stderr, err := runBashSharpCall(t, src)
	qt.Assert(t, qt.IsNotNil(err))
	qt.Assert(t, qt.IsTrue(strings.Contains(stderr, "BASHPP-EASSIGN-CALL")), qt.Commentf("stderr: %s", stderr))
}
