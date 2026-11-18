//go:build full

package interp_test

// Sprint: #209; Story: #461; Story-ID: 4ed649697945
//
// Mechanism-level repairs for the Barrier D EEXPR residue owned by the
// evaluator (ledger destination todo:11f3abf68a13): EEXPR-FORM,
// EEXPR-CONVERT, EEXPR-UNDEFINED and EEXPR-NIL first causes whose roots
// share a cause. Every case is an authored outside-corpus reproducer of one
// mechanism, run through all three controls (native oracle, interpreter,
// lowered artifact) by typedSendThreeModes. The corpus roots each mechanism
// unblocks are named on the case.

import "testing"

func TestGoSourceEExprRepairThreeModes(t *testing.T) {
	cases := map[string]string{
		// EEXPR-FORM "unsupported scalar call": a call whose callee is a
		// function literal arrives with Fun empty and FuncLit set, which the
		// scalar evaluator refused before consulting the callable lookup that
		// already resolves literals. Unblocks func6.go, fixedbugs/issue43444,
		// issue51913, issue20530, issue24937 and the first cause of
		// issue21687 (interpreted).
		"funclit_call_scalar": `package main

var a = false
var order = ""

var _ = func() int { a = true; order += "v"; return 0 }()

func main() {
	if !a {
		panic("package-level literal call did not run")
	}
	if func() bool { order += "c"; return true }() {
		order += "t"
	}
	b := int8(func() int32 { return -1 }())
	u := uint8(b)
	if int32(u) != 255 {
		panic("conversion of literal call result")
	}
	// The tag is evaluated before the case literal runs its side effect,
	// so the mutated comparison must not match (fixedbugs/issue24937).
	x := []byte{'a'}
	switch string(x) {
	case func() string { x[0] = 'b'; return "b" }():
		panic("case literal call ran before the tag")
	}
	println(order)
}
`,
		// The compound-update spelling of the same mechanism: the RHS of
		// `x op= f()` was refused as BASHPP-EUPDATE-RHS wrapping the same
		// EEXPR-FORM, and Go evaluates the update target once — a pointer,
		// slice or map target reassigned by the RHS must still receive the
		// store (fixedbugs/issue21687). The slice case also pins
		// bashPPApplySliceUpdate: the store lands in the payload the target
		// named, not in the variable the RHS rebound.
		"funclit_call_compound_update": `package main

func main() {
	one := 1
	two := 2
	x := &one
	*x += func() int {
		x = &two
		return 0
	}()
	println(one, two)

	sliceOne := []int{1}
	sliceTwo := []int{2}
	s := sliceOne
	s[0] += func() int {
		s = sliceTwo
		return 0
	}()
	println(sliceOne[0], sliceTwo[0])

	mapOne := map[int]int{0: 1}
	mapTwo := map[int]int{0: 2}
	m := mapOne
	m[0] += func() int {
		m = mapTwo
		return 0
	}()
	println(mapOne[0], mapTwo[0])
}
`,
		// EEXPR-FORM "unsupported scalar expression *syntax.BashPPFuncLit": a
		// literal in value position is the closure a declaration would bind,
		// so `Func(func() {})` converts it like any function value. Unblocks
		// fixedbugs/issue24488.
		"funclit_value_conversion": `package main

type Func func()

func (f Func) Run() {
	if f == nil {
		panic("nil")
	}
	f()
}

func main() {
	foo := Func(func() { println("ran") })
	foo.Run()
}
`,
		// EEXPR-UNDEFINED "undefined: a": a declared function named as a
		// value — the operand of a conversion to a named func type — is its
		// handle, exactly as an argument slot binds it. Unblocks
		// fixedbugs/bug294.go and reorder2.go, whose a(s) returns F(a).
		"func_name_as_value": `package main

type F func(s string) F

var log = ""

func a(s string) F {
	log += "a(" + s + ")"
	return F(a)
}

func main() {
	a("1")("2")("3")
	println(log)
}
`,
		// EEXPR-NIL "nil is not a scalar" under a conversion: `[]byte(nil)`
		// is Go's nil slice of the conversion's type — length zero, ""
		// after string conversion, and an index into it panics like any nil
		// slice. Unblocks fixedbugs/bug444.go and issue23814.go.
		"nil_slice_conversion": `package main

func main() {
	println(len([]byte(nil)), string([]byte(nil)) == "", len([]rune(nil)))
	defer func() {
		println(recover() != nil)
	}()
	_ = []byte(nil)[0]
	println("UNREACHABLE")
}
`,
		// EEXPR-CONVERT "cannot convert Complex to float64": a constant
		// complex with a zero imaginary part converts to the real types the
		// way Go's constant conversions do. Unblocks fixedbugs/issue79812.go
		// (T(0 + 0i) with T ~float64).
		"complex_zero_imag_constant": `package main

type Foo interface {
	~float64
}

func f[T Foo](x T) T {
	return T(0 + 0i)
}

func main() {
	got := f(2.0)
	println(got == 0, float64(2+0i) == 2, int(3+0i))
}
`,
		// EEXPR-CONVERT to an interface target: `(J)(t)`, an anonymous
		// `(interface{ M() })(v)`, and an instantiated `I[T](x)` are
		// interface assignments — the checked program guarantees the operand
		// implements them, so the value keeps its dynamic identity in
		// expression position. Unblocks typeparam/issue53477.go and the
		// first causes of fixedbugs/issue18595.go and typeparam/issue47925*.
		"interface_conversion_expression": `package main

type I interface{ M() int }

type T struct{ n int }

func (t *T) M() int { return t.n }

type G[T any] interface{ M() T }

type S struct{}

func (*S) M() *S { return nil }

func f[T G[T]](x T) any { return any(G[T](x)) }

func main() {
	t := &T{7}
	i := (I)(t)
	println(i.M())
	if f[*S](&S{}) == nil {
		panic("boxed conversion lost its value")
	}
	println("ok")
}
`,
		// EASSERT-OPERAND "type assertion operand is not an interface": a
		// concrete pointer returned to an interface result slot must be boxed
		// before named-result settlement and before the caller observes it.
		// The wrong-dynamic-type assertions are negative controls. Unblocks
		// fixedbugs/bug184.go.
		"return_result_interface_boxing": `package main

type Buffer int

func (*Buffer) Read() {}

type Other int

func (*Other) Read() {}

type Reader interface{ Read() }

func f() *Buffer { return nil }

func g() Reader { return f() }

func h() (b *Buffer, ok bool) { return }

func i() (r Reader, ok bool) { return h() }

func main() {
	b := g()
	bb, ok := b.(*Buffer)
	println(bb == nil, ok)
	_, wrong := b.(*Other)
	println(wrong)

	r, flag := i()
	rb, asserted := r.(*Buffer)
	println(rb == nil, flag, asserted)
	_, wrong = r.(*Other)
	println(wrong)
}
`,
		// EEXPR-CONVERT "cannot convert String to Peano": a conversion to a
		// named pointer type is the same identity conversion with the
		// target's name attached, and a runtime nil operand binds as a nil
		// pointer of the target type rather than falling to the scalar
		// carrier. Unblocks fixedbugs/issue4316.go.
		"named_pointer_conversion": `package main

type Peano *Peano

func makePeano(n int) *Peano {
	if n == 0 {
		return nil
	}
	p := Peano(makePeano(n - 1))
	return &p
}

var countArg Peano
var countResult int

func countPeano() {
	if countArg == nil {
		countResult = 0
		return
	}
	countArg = *countArg
	countPeano()
	countResult++
}

func main() {
	var q Peano
	p := Peano(q)
	println(p == nil)
	countArg = makePeano(16)
	countPeano()
	println(countResult)
}
`,
		// EEXPR-UNDEFINED "undefined callable copy": copy used as a value is
		// the same mutation the statement position runs, with its count as
		// the scalar result. Unblocks fixedbugs/issue8620.go.
		"copy_as_scalar": `package main

func main() {
	s1 := []int{1, 2, 3}
	s2 := []int{4, 5}
	if copy(s1, s2) != 2 {
		panic("bad copy count")
	}
	println(s1[0], s1[1], s1[2])
}
`,
		// EEXPR-UNDEFINED "undefined: iota": the RHS of `iota = iota`
		// resolves to the very constant being declared (go.dev/issue/53585),
		// so its written spelling names nothing that exists yet and only the
		// checker's folded value can carry it. Unblocks const8.go.
		"local_iota_redeclared": `package main

const X = 2

func main() {
	const (
		A    = iota
		iota = iota
		B
		C
	)
	println(A, B, C)
	const (
		X = X + X
		Y
		Z = iota
	)
	println(X, Y, Z)
}
`,
		// EEXPR-UNDEFINED "undefined callable error": `error.Error(err)` is a
		// method expression on the predeclared error interface, which no
		// type table lists. Unblocks fixedbugs/issue29304.go.
		"error_method_expression": `package main

import "errors"

func main() {
	err := errors.New("foo")
	println(error.Error(err))
}
`,
		// EEXPR-UNDEFINED "undefined callable make" at a conversion operand:
		// a conversion to a named channel type binds the channel the operand
		// makes, renamed to the target so its methods resolve. Unblocks
		// typeparam/issue47901.go.
		"named_channel_conversion": `package main

type Chan chan int

func (ch Chan) recv() int { return <-ch }

func main() {
	ch := Chan(make(chan int, 1))
	ch <- 7
	println(ch.recv())
}
`,
		// EEXPR-CONVERT "cannot convert String to []byte" in interface
		// position: `[]byte("0")` stored in an interface is the converted
		// slice with the conversion's own type, not a scalar reading of its
		// spelling. Unblocks append.go's first cause.
		"byte_conversion_interface_field": `package main

import "fmt"

type row struct {
	name   string
	value  interface{}
	golden interface{}
}

func main() {
	rows := []row{
		{"bytestr", append([]byte{}, "0"...), []byte("0")},
	}
	fmt.Println(rows[0].name, rows[0].value, rows[0].golden)
}
`,
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) { typedSendThreeModes(t, source) })
	}
}
