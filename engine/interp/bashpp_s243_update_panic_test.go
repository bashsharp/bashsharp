// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

//go:build full

package interp_test

import (
	"strings"
	"testing"

	"github.com/go-quicktest/qt"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Sprint 243 Story #673 (f24307569417): a compound assignment whose operation
// raises a recoverable Go runtime panic (integer divide/remainder by zero) must
// let that panic reach a deferred recover and must leave the target unmodified,
// rather than converting the in-flight panic into a printed BASHPP-EUPDATE-OP
// diagnostic. This is the residue of test/fixedbugs/issue22881.go, whose f6
// (`m[0] /= z`) and f8 (`m[0] %= z`) cases previously reported
// "BASHPP-EUPDATE-OP: scalar call interrupted" instead of panicking.
//
// The three update paths (scalar/pointer, slice element, map element) are all
// exercised so the single-evaluation, no-mutation-on-panic contract holds for
// every compound target shape.
func TestGoSourceCompoundUpdateDivideByZeroRecovers(t *testing.T) {
	t.Parallel()
	const src = `package main

import "fmt"

func main() {
	// map element: the operation panics before the insert, so m stays empty.
	{
		m := map[int]int{}
		func() {
			defer func() { recover() }()
			var z int
			m[0] /= z
			fmt.Println("map divide did not panic")
		}()
		if len(m) != 0 {
			fmt.Println("map insert happened")
		}
	}
	// map element remainder by zero.
	{
		m := map[int]int{}
		func() {
			defer func() { recover() }()
			var z int
			m[0] %= z
			fmt.Println("map remainder did not panic")
		}()
		if len(m) != 0 {
			fmt.Println("map remainder insert happened")
		}
	}
	// slice element: the element keeps its prior value.
	{
		s := []int{7}
		func() {
			defer func() { recover() }()
			var z int
			s[0] /= z
			fmt.Println("slice divide did not panic")
		}()
		if s[0] != 7 {
			fmt.Println("slice element mutated")
		}
	}
	// scalar variable: the variable keeps its prior value.
	{
		x := 9
		func() {
			defer func() { recover() }()
			var z int
			x /= z
			fmt.Println("scalar divide did not panic")
		}()
		if x != 9 {
			fmt.Println("scalar mutated")
		}
	}
	fmt.Println("done")
}
`
	dir := t.TempDir()
	out, errOut, err := bashPPRunGoSource(t, dir, dir+"/update.go", src)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(errOut, ""))
	qt.Assert(t, qt.Equals(out, "done\n"))
	// The internal diagnostic must never surface for a recoverable fault.
	qt.Assert(t, qt.Not(qt.StringContains(out, "BASHPP-EUPDATE-OP")))
}

// The classic (non-GoSource) BashPP dialect has no recoverable runtime panic
// for integer divide by zero, so a compound divide by zero there must still
// surface the OP diagnostic. This pins that the sentinel special-case does not
// swallow ordinary compound-operation errors.
func TestBashPPClassicCompoundDivideByZeroStillDiagnoses(t *testing.T) {
	var out strings.Builder
	r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP), interp.WithBashCompatErrors(true))
	bashPPRun(t, r, "func main() {\n var x int = 6\n var z int = 0\n x /= z\n}\nmain()\n")
	qt.Assert(t, qt.StringContains(out.String(), "BASHPP-EUPDATE-OP:"))
}
