//go:build full

package interp_test

import (
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
)

func TestS219ComplexScalarAndCollections(t *testing.T) {
	source := `package main
import (
	"fmt"
	"math"
	"math/cmplx"
)
type namedComplex complex128
type complexBox struct { value complex128 }
func id(v complex128) complex128 { return v }
func main() {
	z := complex(5.0, 7.0)
	values := append([]complex128{}, z)
	named := append([]namedComplex{}, namedComplex(z))
	fmt.Printf("%T %v %v %v %v %v\n", values[0], values[0], real(values[0]), imag(values[0]), real(named[0]), imag(named[0]))
	zero := complex(0.0, 0.0)
	q := z / zero
	fmt.Println(math.IsInf(real(q), 0), math.IsInf(imag(q), 0), real(q), imag(q))
	returned := id(q)
	fromSlice := []complex128{q}[0]
	fromStruct := complexBox{value: q}.value
	fmt.Println(math.IsInf(real(returned), 0), math.IsInf(imag(returned), 0))
	fmt.Println(math.IsInf(real(fromSlice), 0), math.IsInf(imag(fromSlice), 0))
	fmt.Println(math.IsInf(real(fromStruct), 0), math.IsInf(imag(fromStruct), 0))
	q = complex(2.0, 3.0)
	fmt.Println(q, real(q), imag(q))
	n := cmplx.Sqrt(complex(math.Inf(1), math.NaN()))
	fmt.Println(math.IsInf(real(n), 0), math.IsNaN(imag(n)))
}`
	out, stderr, err := runGoSource(t, "s219complex", source)
	if err != nil {
		t.Fatalf("Runner: %v stdout=%q stderr=%q", err, out, stderr)
	}
	want := "complex128 (5+7i) 5 7 5 7\ntrue true +Inf +Inf\ntrue true\ntrue true\ntrue true\n(2+3i) 2 3\ntrue true\n"
	if out != want || stderr != "" {
		t.Fatalf("stdout=%q stderr=%q; want %q", out, stderr, want)
	}
}

func TestS219ComplexRealImagUntypedConstants(t *testing.T) {
	source := `package main
import "fmt"
func main() {
	fmt.Println(real(0), real(1), real('a'), real(2.0))
	fmt.Println(imag(0), imag(1), imag('a'), imag(2.0))
}`
	_, parseErr := gosource.Parse(strings.NewReader(source), "s219-real-int.go", gosource.Options{RunMain: true})
	if parseErr != nil {
		t.Fatalf("unexpected parse diagnostic: %v", parseErr)
	}
	out, stderr, err := runGoSource(t, "s219-real-untyped", source)
	if err != nil || out != "0 1 97 2\n0 0 0 0\n" || stderr != "" {
		t.Fatalf("err=%v stdout=%q stderr=%q", err, out, stderr)
	}
}

func TestS219ComplexRealTypedIntRejectedByChecker(t *testing.T) {
	source := "package main\nfunc main(){ var x int; _ = real(x) }\n"
	if _, err := gosource.Parse(strings.NewReader(source), "s219-real-typed-int.go", gosource.Options{RunMain: true}); err == nil {
		t.Fatal("Go checker accepted real(x) for typed int x")
	}
}

func TestS219ComplexNonFiniteOperatorsAndTypedConstants(t *testing.T) {
	source := `package main
import (
	"fmt"
	"math"
)
type namedComplex complex128
const c64 complex64 = 1 + 2i
var calls int
func once() complex128 {
	calls++
	return complex(math.Inf(1), math.NaN())
}
func main() {
	fmt.Printf("%T %T %v %v\n", real(c64), imag(c64), real('a'), imag('a'))
	q := once()
	fmt.Println(real(-q), math.IsNaN(imag(+q)), q == q, q != q, calls)
	n := namedComplex(q)
	fmt.Println(math.IsInf(real(n), 0), math.IsNaN(imag(n)))
}`
	out, stderr, err := runGoSource(t, "s219-complex-boundaries", source)
	if err != nil {
		t.Fatalf("Runner: %v stdout=%q stderr=%q", err, out, stderr)
	}
	want := "float32 float32 97 0\n-Inf true false true 1\ntrue true\n"
	if out != want || stderr != "" {
		t.Fatalf("stdout=%q stderr=%q; want %q", out, stderr, want)
	}
}
