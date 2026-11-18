//go:build full

// Sprint: #209; Story: #461; Story-ID: 4ed649697945
//
// The existing interface-comparison coverage remains alongside the arithmetic
// constant-evaluation regression below.
package interp_test

import (
	"strings"
	"testing"

	"github.com/go-quicktest/qt"

	"github.com/bashsharp/bashsharp/engine/gosource"
)

func TestStory461TypedNilInterfaceComparison(t *testing.T) {
	src := `package main
import "fmt"
type box struct{}
func main() {
	fmt.Println(interface{}(nil) == nil)
	fmt.Println(nil == any(nil))
	fmt.Println(interface{}((*box)(nil)) == nil)
	fmt.Println(any((*box)(nil)) != nil)
}`
	out, stderr, err := runGoSource(t, "story461typedniliface", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "true\ntrue\nfalse\ntrue\n"))
}

func TestStory461FuncFieldNilComparison(t *testing.T) {
	src := `package main
import "fmt"
type modifier struct {
	name string
	t    func()
}
func (m modifier) valid() error {
	if m.t == nil {
		return fmt.Errorf("%s missing t", m.name)
	}
	return nil
}
func main() {
	fmt.Println(modifier{name: "zero"}.valid())
	fmt.Println(modifier{name: "live", t: func(){}}.valid())
}`
	out, stderr, err := runGoSource(t, "story461funcfield", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "zero missing t\n<nil>\n"))
}

// These are outside-corpus reductions of shift3 and the float runtime cases.
// The RHS is an untyped constant representable as uint, while the zero divisor
// is a typed runtime float rather than a constant-expression error.
func TestStory461RuntimeFloatAndUntypedShift(t *testing.T) {
	src := `package main
import ("fmt"; "math")
func main() {
	var x int = 1
	zero := float64(0)
	inf := 1 / zero
	fmt.Println(x<<(1+0.), x<<(1+0i), x<<(math.MaxUint+0.), inf, math.IsInf(inf, 1))
}`
	out, stderr, err := runGoSource(t, "story461runtime", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "2 2 0 +Inf true\n"))
}

// A real constant division by zero remains a front-end error. The runtime
// exception above must not become a blanket suppression of DIVZERO checking.
func TestStory461ConstantZeroDivisionRejected(t *testing.T) {
	_, err := gosource.Parse(strings.NewReader("package main\nconst bad = 1.0 / 0.0\nfunc main(){}\n"), "story461negative.go", gosource.Options{RunMain: true})
	qt.Assert(t, qt.IsNotNil(err))
	qt.Assert(t, qt.StringContains(err.Error(), "division by zero"))
}

func TestStory461StructuredConversionCarriers(t *testing.T) {
	src := `package main
import (
	"fmt"
	"unsafe"
)
type pair[F1, F2 any] struct { f1 F1; f2 F2 }
type mypair struct { f1 int32; f2 int64 }
func main() {
	s := []byte{0, 1, 2, 3}
	fmt.Println([4]byte(s) == *(*[4]byte)(s))
	p := pair[int32, int64]{1, 2}
	mp := mypair(p)
	fmt.Println(mp.f1, mp.f2, unsafe.Sizeof(p.f1), unsafe.Sizeof(p.f2))
}`
	out, stderr, err := runGoSource(t, "story461structuredconversions", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "true\n1 2 4 8\n"))
}

func TestStory461BadStructuredConversionRejected(t *testing.T) {
	src := `package main
type A struct { x int }
type B struct { x string }
func main() {
	_ = B(A{x: 1})
}`
	_, err := gosource.Parse(strings.NewReader(src), "story461badconversion.go", gosource.Options{RunMain: true})
	qt.Assert(t, qt.IsNotNil(err))
	qt.Assert(t, qt.StringContains(err.Error(), "cannot convert"))
}
