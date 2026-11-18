//go:build full

package interp_test

// Sprint: #243; Story: #671; Story-ID: 56d156f9118e
//
// Collection semantics the Go corpus reached in wave 7: a nil-map write is
// the recoverable panic Go raises, and the assignments before it in the same
// statement have already taken effect; a dense zero-size slice grows to its
// exact new length; an array copy
// has the array's own capacity; and a range over a collection conversion
// ranges the slice it builds, evaluating the operand once. Every case runs
// the unchanged original in the SDK, the interpreter and the lowered
// artifact, and compares the three.

import "testing"

func TestGoSourceNilMapAssignmentPanics(t *testing.T) {
	cases := map[string]string{
		"tuple_assign_left_to_right": `package main

import "fmt"

var g int

func main() {
	func() {
		var m map[int]int
		var a int
		p := &a
		defer func() {
			fmt.Println(recover(), *p)
		}()
		*p, m[2] = 5, 2
	}()
	func() {
		var m map[int]int
		defer func() {
			fmt.Println(recover(), g)
		}()
		m[0], g = 1, 2
	}()
	func() {
		var m = map[int]int{}
		var p *int
		defer func() {
			fmt.Println(recover(), len(m), m[2])
		}()
		m[2], *p = 42, 2
	}()
}
`,
		"recovered_value_is_runtime_error": `package main

import (
	"fmt"
	"runtime"
)

type S struct{ m map[string]int }

func main() {
	defer func() {
		v := recover()
		err, isError := v.(error)
		_, isRuntime := v.(runtime.Error)
		fmt.Println(isError, isRuntime, err)
	}()
	var s S
	s.m["k"] = 1
	fmt.Println("UNREACHABLE")
}
`,
		"nil_temporary_takes_target_type": `package main

import "fmt"

type T struct{ x struct{ y int } }

func main() {
	var x T
	p := &x
	p, p.x.y = nil, 7
	fmt.Println(x.x.y, p == nil)
	m := map[string]int{"a": 1}
	s := []int{1, 2}
	var f func() int = func() int { return 3 }
	m, s[0], s = nil, 9, nil
	fmt.Println(m == nil, s == nil)
	f, x.x.y = nil, 8
	fmt.Println(f == nil, x.x.y)
	var e error = fmt.Errorf("e")
	e, x.x.y = nil, 9
	fmt.Println(e == nil, x.x.y)
	q := &x
	q, q.x.y = new(T), 4
	fmt.Println(x.x.y, q.x.y)
}
`,
		"index_assign_and_compound": `package main

import "fmt"

func main() {
	func() {
		defer func() { fmt.Println(recover()) }()
		var m map[string]int
		m["a"] = 1
	}()
	func() {
		defer func() { fmt.Println(recover()) }()
		var m map[string]int
		m["a"] += 1
	}()
	fmt.Println("done")
}
`,
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) { typedSendThreeModes(t, source) })
	}
}

func TestS243DenseZeroSizeSliceCapacity(t *testing.T) {
	typedSendThreeModes(t, `package main
import "fmt"
type T struct{}
func main() {
 s := make([]T, 2, 5)
 s = append(s, T{})
 fmt.Println(len(s), cap(s), s)
 s = append(s, T{}, T{}, T{})
 fmt.Println(len(s), cap(s), s)
 var z []T
 z = append(z, s...)
 fmt.Println(len(z), cap(z), z == nil)
}`)
}

func TestGoSourceArrayCopyCapacity(t *testing.T) {
	cases := map[string]string{
		"copied_array_reslices_to_its_length": `package main

import "fmt"

type S struct{ a [70]int }

func mk() [70]int { var a [70]int; return a }

func main() {
	var x [64]byte
	for i := range x {
		x[i] = byte(i)
	}
	y := x
	fmt.Println(len(y), cap(y), cap(y[:]), cap(y[4:36]))
	copy(x[4:36], x[2:34])
	*(*[32]byte)(y[4:36]) = *(*[32]byte)(y[2:34])
	fmt.Println(x == y)
	var s S
	t := s
	m := mk()
	p := &x
	q := *p
	arr := [3][70]int{}
	row := arr[1]
	fmt.Println(cap(t.a), cap(m), cap(q), cap(row), cap(t.a[:]), cap(m[:]), cap(q[:]), cap(row[:]))
}
`,
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) { typedSendThreeModes(t, source) })
	}
}

func TestGoSourceRangeOverConversion(t *testing.T) {
	cases := map[string]string{
		"operand_evaluated_once": `package main

import "fmt"

var nmake int

func makenumstring() string {
	nmake++
	return "\x01\x02\x03\x04\x05"
}

func main() {
	s := byte(0)
	for _, v := range []byte(makenumstring()) {
		s += v
	}
	fmt.Println(nmake, s)
	nmake = 0
	n := 0
	for i := range []rune(makenumstring()) {
		n += i
	}
	fmt.Println(nmake, n)
}
`,
		"bytes_runes_and_named": `package main

import "fmt"

type Bytes []byte

func text() string { return "héy" }

func main() {
	str := "héy"
	for i, v := range []byte(str) {
		fmt.Println(i, v)
	}
	for i, v := range []rune(str + "!") {
		fmt.Println(i, v, string(v))
	}
	for i, v := range []byte("ab") {
		fmt.Println(i, v)
	}
	for i, v := range Bytes(text()) {
		fmt.Println(i, v)
	}
	for i, v := range string([]byte("ok")) {
		fmt.Println(i, v)
	}
	for i := range []byte(text()) {
		fmt.Println(i)
	}
}
`,
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) { typedSendThreeModes(t, source) })
	}
}
