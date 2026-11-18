//go:build full

// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp_test

import (
	"testing"

	"github.com/go-quicktest/qt"
)

// Sprint: #219; Story: #460; Story-ID: d8e7d58f362b
//
// Func-valued CALL RESULTS as collection elements. The call's result cell
// carries a closure handle in its scalar slot; the element path must bind it
// against the func-typed element like a named callable instead of rejecting
// the scalar. Shapes follow typeparam/issue58513 (generic func returning a
// func, generic method value) and fixedbugs/issue59680 (closure through a
// declared func type).

func TestS219CallableCollectionGenericCallResult(t *testing.T) {
	const src = `package main
import "fmt"
func assert[_ any]() { panic(0) }
func Assert[To any]() func() { return assert[To] }
type asserter[_ any] struct{}
func (asserter[_]) assert() { panic(1) }
func AssertMV[To any]() func() { return asserter[To]{}.assert }
func AssertME[To any]() func(asserter[To]) { return asserter[To].assert }
var tests = []func(){
	Assert[int](),
	AssertMV[int](),
	func() { me := AssertME[string](); me(asserter[string]{}) },
}
func main() {
	n := 0
	for _, test := range tests {
		func() {
			defer func() {
				if recover() != nil {
					n++
				}
			}()
			test()
		}()
	}
	fmt.Println(len(tests), n)
}
`
	out, stderr, err := runGoSource(t, "s219callgeneric", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "3 3\n"))
}

func TestS219CallableCollectionDeclaredFuncTypeCallResult(t *testing.T) {
	const src = `package main
import "fmt"
type B struct {
	pid int
	f   func() (uint64, error)
}
func Sq(i int) uint64 { return uint64(i * i) }
type RO func(*B)
var ROSL = []RO{
	Bad(),
}
func Bad() RO {
	return func(b *B) {
		b.f = func() (uint64, error) {
			return Sq(b.pid), nil
		}
	}
}
func main() {
	b := &B{pid: 17}
	for _, opt := range ROSL {
		opt(b)
	}
	v, err := b.f()
	fmt.Println(v, err)
}
`
	out, stderr, err := runGoSource(t, "s219calldeclared", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "289 <nil>\n"))
}

func TestS219CallableCollectionLocalCallResultAppend(t *testing.T) {
	const src = `package main
import "fmt"
func adder(n int) func(int) int { return func(x int) int { return x + n } }
func main() {
	var fs []func(int) int
	fs = append(fs, adder(1))
	fs = append(fs, adder(10), adder(100))
	local := []func(int) int{adder(1000)}
	m := map[string]func(int) int{"k": adder(5)}
	sum := 0
	for _, f := range fs {
		sum += f(1)
	}
	fmt.Println(sum, local[0](1), m["k"](1))
}
`
	out, stderr, err := runGoSource(t, "s219callappend", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "114 1001 6\n"))
}

// fixedbugs/issue49512: the appended func parameter arrived through an
// interface method call, bound from a method value on the caller's side.
func TestS219CallableCollectionInterfaceMethodParamAppend(t *testing.T) {
	const src = `package main
import "fmt"
type S struct{ m1Called, m2Called bool }
func (s *S) M1(int) (int, int) { s.m1Called = true; return 0, 0 }
func (s *S) M2(int) (int, int) { s.m2Called = true; return 0, 0 }
type C struct {
	calls []func(int) (int, int)
}
func makeC() Funcs { return &C{} }
func (c *C) Add(fn func(int) (int, int)) Funcs {
	c.calls = append(c.calls, fn)
	return c
}
func (c *C) Call() {
	for _, fn := range c.calls {
		fn(0)
	}
}
type Funcs interface {
	Add(func(int) (int, int)) Funcs
	Call()
}
func main() {
	s := &S{}
	c := makeC().Add(s.M1).Add(s.M2)
	c.Call()
	fmt.Println(s.m1Called, s.m2Called)
}
`
	out, stderr, err := runGoSource(t, "s219ifaceparam", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "true true\n"))
}

// A closure appended through a call result keeps its identity: the same
// handle is stored rather than a fresh clone, so captured state is shared by
// every element that holds it, as it is in Go.
func TestS219CallableCollectionCallResultIdentity(t *testing.T) {
	const src = `package main
import "fmt"
func counter() func() int {
	n := 0
	return func() int { n++; return n }
}
func main() {
	c := counter()
	var fs []func() int
	fs = append(fs, c)
	fs = append(fs, fs[0])
	lit := []func() int{c, counter()}
	fmt.Println(fs[0](), fs[1](), c(), lit[0](), lit[1]())
}
`
	out, stderr, err := runGoSource(t, "s219callidentity", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "1 2 3 4 1\n"))
}
