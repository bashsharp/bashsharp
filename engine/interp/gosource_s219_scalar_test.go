//go:build full

package interp_test

// Sprint: #219; Story: #461; Story-ID: 4ed649697945
//
// Scalar conversion and numeric evaluation repairs: the runtime-versus-
// constant float distinction, the NaN and infinity carriers, untyped float
// constants in shifts, predeclared aliases in switch cases, and the spelling
// a conversion target arrives under. Every positive case is an authored
// outside-corpus reproducer run through the three controls by
// typedSendThreeModes; the corpus roots each unblocks are named on the case.
// The negatives pin the constant-time rejections the repairs must keep.

import (
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
)

func TestS219ScalarNumericThreeModes(t *testing.T) {
	cases := map[string]string{
		// convinline.go: `float32(v)` on a runtime float64 beyond float32's
		// range is the infinity of its sign, where the same conversion of a
		// constant is a compile-time overflow.
		"runtime_float32_overflow": `package main

import "math"

func convert1[T int64 | uint64 | float64](v T) string {
	f := float32(v)
	if math.IsInf(float64(f), -1) {
		return "float32(math.Inf(-1))"
	}
	if math.IsInf(float64(f), +1) {
		return "float32(math.Inf(+1))"
	}
	return "finite"
}

func main() {
	println(convert1(-1.79769e+308))
	println(convert1(1.79769e+308))
	println(convert1(int64(1)))
	v := 3.5e38
	f := float32(v)
	println(math.IsInf(float64(f), 1))
	g := float32(-2.0)
	println(float64(g))
	d := float64(f)
	println(math.IsInf(d, 1), float32(d) > 0)
	w := -v * 10
	println(math.IsInf(float64(float32(w)), -1))
}
`,
		// floatcmp.go and zerodivide.go: math.NaN() and math.Inf() land in
		// float64 variables as the runtime non-finite scalar rather than as
		// a bridge object, so comparisons and arithmetic on them follow IEEE.
		"nonfinite_variables": `package main

import "math"

var nan float64 = math.NaN()
var f float64 = 1

type floatTest struct {
	name string
	expr bool
	want bool
}

var tests = []floatTest{
	{"nan == nan", nan == nan, false},
	{"nan != nan", nan != nan, true},
	{"nan < nan", nan < nan, false},
	{"f < nan", f < nan, false},
	{"f >= nan", f >= nan, false},
	{"nan > f", nan > f, false},
	{"nan != f", nan != f, true},
}

func main() {
	inf := math.Inf(1)
	negInf := math.Inf(-1)
	println(inf > f, negInf < f, inf == inf, -inf == negInf)
	println(math.IsNaN(nan), math.IsInf(inf, 1), math.IsInf(negInf, -1))
	for _, t := range tests {
		if t.expr != t.want {
			println("FAIL", t.name)
		}
	}
	x := nan
	println(x != x, math.IsNaN(x+1), math.IsInf(inf*2, 1), math.IsNaN(inf-inf))
	var g64 float64
	println(math.IsInf(inf/g64, 1), math.IsInf(negInf/g64, -1), math.IsNaN(nan/g64))
	println(float32(inf) > 0, math.IsInf(float64(float32(negInf)), -1))
	println("done")
}
`,
		// const.go: an untyped float constant with an integer value shifts as
		// that integer.
		"untyped_float_constant_shift": `package main

import "fmt"

const (
	chuge   = 1 << 100
	c1      = chuge >> 100
	rsh1    = 1e100 >> 1000
	rsh2    = 1e302 >> 1000
	lsh     = 1e3 << 2
	fhuge   float64 = 1 << 100
	f1      float64 = chuge >> 100
	chuge_1 = chuge - 1
)

func main() {
	fmt.Println(c1, rsh1, rsh2, lsh)
	fmt.Println(fhuge == 1267650600228229401496703205376.0, f1, chuge_1 == 1267650600228229401496703205375)
	fmt.Println(string(rune(456)), string(int(123)) == "{")
}
`,
		// turing.go: `switch prog[pc]` tags a uint8 while the case constants
		// take the alias byte; the two spell one type.
		"alias_switch_tag": `package main

const prog = "+>."

var a [3]byte

func main() {
	pc := 0
	out := ""
	for pc < len(prog) {
		switch prog[pc] {
		case '+':
			a[0]++
			out += "plus "
		case '>':
			out += "right "
		case '.':
			out += "dot"
		}
		pc++
	}
	println(out, a[0])
	var b byte = 'x'
	switch b {
	case uint8('x'):
		println("alias match")
	}
	var r rune = 'y'
	switch r {
	case int32('y'):
		println("rune match")
	default:
		println("rune miss")
	}
	var u uint8 = 200
	switch u {
	case byte(199):
		println("wrong")
	case byte(200):
		println("byte match")
	}
}
`,
		// fixedbugs/issue29329: a defined slice type converts a method call's
		// result, which no named operand carries. The call runs exactly once
		// and its payload is shared as Go shares it.
		"defined_slice_conversion_of_call": `package main

import "fmt"

type Point [2]float64
type LineString []Point
type MultiPoint []Point

func (mp MultiPoint) Clone() MultiPoint {
	if mp == nil {
		return nil
	}
	points := make([]Point, len(mp))
	copy(points, mp)
	return MultiPoint(points)
}

func (ls LineString) Clone() LineString {
	ps := MultiPoint(ls)
	return LineString(ps.Clone())
}

func data() LineString { return LineString{{1.0, 2.0}, {3, 4}} }

type bytesOf []byte

var calls int

func text() string { calls++; return "hi" }

type ints []int

func (xs ints) Sum() int {
	s := 0
	for _, x := range xs {
		s += x
	}
	return s
}

func numbers() []int { calls++; return []int{1, 2, 3} }

func main() {
	ls := data()
	c := ls.Clone()
	fmt.Println(len(c), c[0][0], c[1][1], len(ls))
	var nilLs LineString
	fmt.Println(nilLs.Clone() == nil)
	b := bytesOf(text())
	fmt.Println(len(b), string(b), calls)
	fmt.Println(ints(numbers()).Sum(), calls)
	shared := ints(numbers())
	shared[0] = 10
	fmt.Println(shared.Sum(), calls)
}
`,
		// fixedbugs/issue30606b and typeparam/issue54456: a parenthesized
		// or instantiated conversion target names the type it converts to,
		// not its source spelling.
		"conversion_target_spelling": `package main

import "fmt"

type T[_ any] int

func main() {
	b := (byte)(65)
	fmt.Println(b, (int)(b)+1, (float64)(b)/2, (string)(rune(b)))
	x := T[int](7)
	fmt.Println(int(x)+1, x == 7)
	var y float32 = (float32)(1.5)
	fmt.Println(y * 2)
}
`,
		// fixedbugs/issue58671: a variadic parameter inferred or declared
		// as float64 converts each exact untyped argument to the runtime
		// float it receives.
		"variadic_float_arguments": `package main

import "fmt"

func g[P any](xs ...P) P { var zero P; return zero }

func sum(xs ...float64) float64 {
	s := 0.0
	for _, x := range xs {
		s += x
	}
	return s
}

var (
	_ int        = g(1, 2)
	_ rune       = g(1, 'a')
	_ float64    = g(1, 'a', 2.3)
	_ float64    = g('a', 2.3)
	_ complex128 = g(2.3, 'a', 1i)
)

func main() {
	fmt.Println(g(1, 'a', 2.3), g(1, 2))
	fmt.Println(sum(1, 2.5, 'a'))
	fmt.Println(sum())
	x := 0.1
	fmt.Println(sum(x, 0.2) == 0.30000000000000004, sum(2.3) == 2.3)
}
`,
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) { typedSendThreeModes(t, source) })
	}
}

// The rejections the runtime float and shift repairs keep: the classic
// dialect still reports a float32 overflow for a variable's value, a constant
// with a fraction is still not shiftable, and the classic shift rule is
// unchanged.
func TestS219ScalarNumericRejections(t *testing.T) {
	tests := []struct{ name, src, want string }{
		{"classic float32 overflow", "func main() {\n n := 999999999999999999999999999999999999999.0\n var f float32 = n\n}\nmain()\n", "overflows float32"},
		{"fractional shift", "func main() {\n x := 1.5 << 1\n echo \"$x\"\n}\nmain()\n", "BASHPP-EEXPR-SHIFT"},
		{"classic float shift", "func main() {\n x := 1e2 >> 1\n echo \"$x\"\n}\nmain()\n", "BASHPP-EEXPR-SHIFT: shift requires integer operands"},
		{"switch kind mismatch", "func main() { switch 1 { case \"1\": echo wrong } }\nmain()\n", "BASHPP-ESWITCH-TYPE: case expression type String does not match switch tag type Int"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, stderr, err := runBashSharpCall(t, tc.src)
			if err == nil || !strings.Contains(stderr, tc.want) {
				t.Fatalf("stderr=%q err=%v", stderr, err)
			}
		})
	}
}

// A Go constant conversion that overflows float32 never reaches the
// evaluator's runtime rounding: the front end rejects it as Go does.
func TestS219ConstantFloat32OverflowRejected(t *testing.T) {
	for name, src := range map[string]string{
		"conversion": "package main\nconst c = 1e39\nvar f = float32(c)\nfunc main() { println(f) }\n",
		"literal":    "package main\nfunc main() { f := float32(-1.79769e+308); println(f) }\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := gosource.Parse(strings.NewReader(src), name+".go", gosource.Options{RunMain: true})
			if err == nil || !strings.Contains(err.Error(), "cannot convert") {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

// TestS219SignedZeroRegression is an outside-corpus reduction of
// fixedbugs/issue27718.go. Signed zero and non-finite provenance must survive
// both parameter and result cells; each argument producer is evaluated once.
// The integer zero-divisor negative keeps that runtime-float exception narrow.
func TestS219SignedZeroRegression(t *testing.T) {
	typedSendThreeModes(t, `package main

import "math"

var calls int

func source64(x float64) float64 { calls++; return x }
func source32(x float32) float32 { calls++; return x }

func add64(x float64) float64 { return x + 0 }
func sub64(x float64) float64 { return x - 0 }
func neg64(x float64) float64 { return -x }
func add32(x float32) float32 { return x + 0 }
func sub32(x float32) float32 { return x - 0 }
func neg32(x float32) float32 { return -x }

func rejectIntegerDivideByZero() {
	defer func() { println(recover() != nil) }()
	var zero int
	_ = 1 / zero
	println(false)
}

func main() {
	zero := float64(0)
	inf := 1.0 / zero
	negZero64 := -1 / inf
	negZero32 := float32(negZero64)
	println(
		1/add64(negZero64) == inf,
		math.IsInf(1/sub64(negZero64), -1),
		1/neg64(negZero64) == inf,
	)
	println(
		math.IsInf(float64(1/add32(negZero32)), 1),
		math.IsInf(float64(1/sub32(negZero32)), -1),
		math.IsInf(float64(1/neg32(negZero32)), 1),
	)
	println(source64(1), source32(2), calls)
	rejectIntegerDivideByZero()
}
`)
}
