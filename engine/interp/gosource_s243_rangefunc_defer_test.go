// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

//go:build full

package interp_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/go-quicktest/qt"
)

// range4.go ends with testcalls1, whose iter3 iterator accepts the defined
// yield type iter3YieldFunc. Keep the upstream fixture unmodified so admission
// of that named function type is exercised together with all preceding range
// protocol checks in native, interpreted, and compiled modes.
func TestGoSourceRange4OriginalThreeModes(t *testing.T) {
	source, err := os.ReadFile(filepath.Join(runtime.GOROOT(), "test", "range4.go"))
	qt.Assert(t, qt.IsNil(err))
	typedSendThreeModes(t, string(source))
}

// Sprint 243 Story #673 (f24307569417): a defer executed inside a
// range-over-function body binds to the ENCLOSING function, not to the yield
// callback or the iterator frame. These are the corpus behaviours range4.go and
// fixedbugs/issue71675.go pin — LIFO order at the enclosing return, yield
// effect/order, early stop, panic/recover across the iterator, and nesting —
// exercised directly through the interpreted GoSource evaluator.

// gosourceRangeDeferEnclosingLIFO is func7 from range4.go: the body's defers run
// last-in-first-out when func7 returns, interleaved by registration order with
// the defers written around the loop, not when the iterator returns.
const gosourceRangeDeferEnclosingLIFO = `package main

import "fmt"

var saved []int

func save(x int) { saved = append(saved, x) }

func yield4(yield func(int) bool) {
	_ = yield(1) && yield(2) && yield(3) && yield(4)
}

func func7() {
	defer save(-1)
	for i := range yield4 {
		defer save(i)
	}
	defer save(5)
}

func main() {
	func7()
	fmt.Println(saved)
}
`

const gosourceRangeDeferEnclosingLIFOWant = "[5 4 3 2 1 -1]\n"

func TestGoSourceRangeDeferEnclosingLIFO(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	out, errOut, err := bashPPRunGoSource(t, dir, dir+"/func7.go", gosourceRangeDeferEnclosingLIFO)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(errOut, ""))
	qt.Assert(t, qt.Equals(out, gosourceRangeDeferEnclosingLIFOWant))
}

// gosourceRangeDeferNested is func8 from range4.go: two iterators, one nested in
// the other's body, all of whose body defers belong to func8 and unwind in one
// LIFO sequence — the sharpest test that nesting routes each buffer onto the
// enclosing frame in registration order.
const gosourceRangeDeferNested = `package main

import "fmt"

var saved []int

func save(x int) { saved = append(saved, x) }

func yield2(yield func(int) bool) { _ = yield(1) && yield(2) }
func yield3(yield func(int) bool) { _ = yield(1) && yield(2) && yield(3) }
func yield4(yield func(int) bool) { _ = yield(1) && yield(2) && yield(3) && yield(4) }

func func8() {
	defer save(-1)
	for i := range yield2 {
		for j := range yield3 {
			defer save(i*10 + j)
		}
		defer save(i)
	}
	defer save(-2)
	for i := range yield4 {
		defer save(i)
	}
	defer save(-3)
}

func main() {
	func8()
	fmt.Println(saved)
}
`

const gosourceRangeDeferNestedWant = "[-3 4 3 2 1 -2 2 23 22 21 1 13 12 11 -1]\n"

func TestGoSourceRangeDeferNested(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	out, errOut, err := bashPPRunGoSource(t, dir, dir+"/func8.go", gosourceRangeDeferNested)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(errOut, ""))
	qt.Assert(t, qt.Equals(out, gosourceRangeDeferNestedWant))
}

// gosourceRangeDeferEarlyStop breaks out of the loop after the second yield: the
// defers registered before the break still belong to the enclosing function and
// unwind with the ones written around the loop. It also pins that the yield
// effect stops at the break — only 1 and 2 were saved from the body.
const gosourceRangeDeferEarlyStop = `package main

import "fmt"

var saved []int

func save(x int) { saved = append(saved, x) }

func yield4(yield func(int) bool) {
	_ = yield(1) && yield(2) && yield(3) && yield(4)
}

func stopper() {
	defer save(-1)
	for i := range yield4 {
		defer save(i)
		if i == 2 {
			break
		}
	}
	defer save(9)
}

func main() {
	stopper()
	fmt.Println(saved)
}
`

const gosourceRangeDeferEarlyStopWant = "[9 2 1 -1]\n"

func TestGoSourceRangeDeferEarlyStop(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	out, errOut, err := bashPPRunGoSource(t, dir, dir+"/stopper.go", gosourceRangeDeferEarlyStop)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(errOut, ""))
	qt.Assert(t, qt.Equals(out, gosourceRangeDeferEarlyStopWant))
}

// gosourceRangeDeferPanicRecover is h() from fixedbugs/issue71675.go: the second
// iterator panics after its yield, and the body's deferred recover — bound to h,
// not to the iterator — catches it while the surrounding defers unwind in LIFO
// order. The wrong binding ran "H second" early and left the panic uncaught.
const gosourceRangeDeferPanicRecover = `package main

import "fmt"

func yieldInts(yield func(int) bool) {
	if !yield(0) {
		return
	}
}

func yieldIntsPanic(yield func(int) bool) {
	if !yield(0) {
		return
	}
	panic("yield stop")
}

func h() {
	defer func() {
		fmt.Println("H first")
	}()
	for range yieldInts {
		defer func() {
			fmt.Println("H second")
		}()
	}
	defer func() {
		fmt.Println("H third")
	}()
	for range yieldIntsPanic {
		defer func() {
			fmt.Println("h recover:called")
			recover()
		}()
	}
}

func main() {
	h()
	fmt.Println("h returned")
}
`

const gosourceRangeDeferPanicRecoverWant = `h recover:called
H third
H second
H first
h returned
`

func TestGoSourceRangeDeferPanicRecover(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	out, errOut, err := bashPPRunGoSource(t, dir, dir+"/h.go", gosourceRangeDeferPanicRecover)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(errOut, ""))
	qt.Assert(t, qt.Equals(out, gosourceRangeDeferPanicRecoverWant))
}

// gosourceRangeDeferDeadReturn is i() from fixedbugs/issue71675.go: a panic
// after the loop is recovered by the body's deferred recover even though the
// panic makes the return block dead code. The deferred cleanup must still run
// and still recover, so the caller resumes normally.
const gosourceRangeDeferDeadReturn = `package main

import "fmt"

func yieldInts(yield func(int) bool) {
	if !yield(0) {
		return
	}
}

func i() {
	for range yieldInts {
		defer func() {
			fmt.Println("I")
			recover()
		}()
	}
	panic("i panic")
}

func main() {
	i()
	fmt.Println("i returned")
}
`

const gosourceRangeDeferDeadReturnWant = "I\ni returned\n"

func TestGoSourceRangeDeferDeadReturn(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	out, errOut, err := bashPPRunGoSource(t, dir, dir+"/i.go", gosourceRangeDeferDeadReturn)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(errOut, ""))
	qt.Assert(t, qt.Equals(out, gosourceRangeDeferDeadReturnWant))
}
