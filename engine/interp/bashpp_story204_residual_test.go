// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp_test

import (
	"context"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/go-quicktest/qt"
)

func TestBashPPTupleAssignAtomicSemantics(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{"swap uses old RHS values", "var x int = 1\nvar y int = 2\nx, y = y, x\nprintf '%s:%s' \"$x\" \"$y\"", "2:1"},
		{"duplicate LHS commits left to right", "var x int = 1\nx, x = 2, 3\nprintf '%s' \"$x\"", "3"},
		{"typed mismatch is atomic", "var x int = 1\nvar y bool = true\nx, y = 2, \"bad\"\nprintf '|%s|%s|' \"$x\" \"$y\"", "BASHPP-EASSIGN-TYPE: cannot assign String to bool\n|1|true|"},
		{"undeclared target is atomic", "var x int = 1\nx, missing = 2, 3\nprintf '|%s|%s|' \"$x\" \"${missing-unset}\"", "BASHPP-EASSIGN-UNDECLARED: assignment target missing is not declared\n|1|unset|"},
		{"arity is rejected", "var x int = 1\nvar y int = 2\nx, y = 3\nprintf '%s:%s' \"$x\" \"$y\"", "BASHPP-EASSIGN-ARITY: 2 variable(s) but 1 value(s)\n1:2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out strings.Builder
			r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP))
			bashPPRun(t, r, "func main() {\n"+tc.body+"\n}\nmain()\n")
			qt.Assert(t, qt.Equals(out.String(), tc.want))
		})
	}
}

func TestBashPPPackageInitOrderPolicy(t *testing.T) {
	const src = "var first int = second\nvar second int = 2\n"
	var out strings.Builder
	r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP), interp.WithBashCompatErrors(true))
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "init.bpp")
	qt.Assert(t, qt.IsNil(err))
	err = r.Run(context.Background(), file)
	qt.Assert(t, qt.ErrorIs(err, interp.ExitStatus(2)))
	qt.Assert(t, qt.StringContains(out.String(), "init.bpp: line 1: BASHPP-EINIT-ORDER:"))
}

func TestBashPPTupleAssignFunctionResultsAtomic(t *testing.T) {
	const src = "func pair() (int, bool) { return 7, true }\nfunc main() {\nvar x int = 1\nvar y bool = false\nx, y = pair()\nprintf '%s:%s' \"$x\" \"$y\"\n}\nmain()\n"
	var out strings.Builder
	r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP))
	bashPPRun(t, r, src)
	qt.Assert(t, qt.Equals(out.String(), "7:true"))
}

func TestBashPPTupleAssignFunctionTypeFailureRollsBack(t *testing.T) {
	const src = "func pair() (int, bool) { return 7, true }\nfunc main() {\nvar x int = 1\nvar y int = 2\nx, y = pair()\nprintf '|%s:%s|' \"$x\" \"$y\"\n}\nmain()\n"
	var out strings.Builder
	r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP))
	bashPPRun(t, r, src)
	qt.Assert(t, qt.StringContains(out.String(), "BASHPP-EASSIGN-TYPE: cannot assign bool to int\n|1:2|"))
}

func TestBashPPTupleAssignPreservesRichCells(t *testing.T) {
	const src = `type Box struct { N int }
func main() {
 left := make(chan int, 1)
 right := make(chan int, 1)
 left <- 1
 right <- 2
 left, right = right, left
 gotLeft := <-left
 gotRight := <-right
 a := Box{N: 1}
 b := Box{N: 2}
 a, b = b, a
 p := new(Box)
 q := new(Box)
 p.N = 3
 q.N = 4
 p, q = q, p
 f := func() { printf f }
 g := func() { printf g }
 f, g = g, f
 pv := *p
 qv := *q
 printf '%s:%s:%s:%s:%s:' "$gotLeft" "$gotRight" a.N pv.N qv.N
 f()
 g()
}
main()
`
	var out strings.Builder
	r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP))
	bashPPRun(t, r, src)
	qt.Assert(t, qt.Equals(out.String(), "2:1:2:4:3:gf"))
}

func TestBashPPTupleAssignGenericResultsAndSpacedExpression(t *testing.T) {
	const src = `func pair[T any](a, b T) (T, T) {
 return a, b
}
func main() {
 var x int = 1
 var y int = 2
 x, y = pair[int](7, 8)
 x = x + 1
 printf '%s:%s' "$x" "$y"
}
main()
`
	var out strings.Builder
	r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP))
	bashPPRun(t, r, src)
	qt.Assert(t, qt.Equals(out.String(), "8:8"))
}

func TestBashPPTupleAssignFunctionResultsPreserveMetadata(t *testing.T) {
	const src = `type Box struct { N int }
func rich() (*Box, *Box) {
 p := new(Box)
 q := new(Box)
 p.N = 5
 q.N = 6
 return p, q
}
func main() {
 var p *Box
 var q *Box
 p, q = rich()
 pv := *p
 qv := *q
 printf '%s:%s' pv.N qv.N
}
main()
`
	var out strings.Builder
	r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP))
	bashPPRun(t, r, src)
	qt.Assert(t, qt.Equals(out.String(), "5:6"))
}

func TestBashPPTupleAssignMetadataRollback(t *testing.T) {
	const src = `func main() {
 left := make(chan int, 1)
 right := make(chan int, 1)
 left <- 1
 right <- 2
 var n int = 3
 left, n = right, "bad"
 gotLeft := <-left
 gotRight := <-right
 printf '|%s:%s:%s|' "$gotLeft" "$gotRight" "$n"
}
main()
`
	var out strings.Builder
	r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP))
	bashPPRun(t, r, src)
	qt.Assert(t, qt.StringContains(out.String(), "BASHPP-EASSIGN-TYPE: cannot assign String to int\n|1:2:3|"))
}

func TestBashPPTupleAssignMalformedRHSIsPositioned(t *testing.T) {
	const src = "func main() {\nvar x int = 1\nvar y int = 2\nx, y = x+, y+\nprintf '|%s:%s|' \"$x\" \"$y\"\n}\nmain()\n"
	var out strings.Builder
	r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP), interp.WithBashCompatErrors(true))
	bashPPRun(t, r, src)
	qt.Assert(t, qt.StringContains(out.String(), "line 4: BASHPP-EASSIGN-FORM:"))
	qt.Assert(t, qt.StringContains(out.String(), "|1:2|"))
}

func TestBashPPTupleAssignNamedRichResultsPreserveMetadata(t *testing.T) {
	const src = `type Box struct { N int }
type Ch int
func rich() (ptr *Box, value Box, pipe Ch) {
 p := new(Box)
 p.N = 5
 b := Box{N: 6}
 ch := make(chan int, 1)
 ch <- 7
 return p, b, ch
}
func main() {
 var p *Box
 var b Box
 var ch Ch
 p, b, ch = rich()
 pv := *p
 got := <-ch
 printf '%s:%s:%s' pv.N b.N "$got"
}
main()
`
	var out strings.Builder
	r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP))
	bashPPRun(t, r, src)
	qt.Assert(t, qt.Equals(out.String(), "5:6:7"))
}

func TestBashPPTupleAssignFromConstDoesNotFreezeDestination(t *testing.T) {
	const src = `func main() {
 const source int = 2
 var target int = 1
 target = source
 target = 3
 printf '%s' "$target"
}
main()
`
	var out strings.Builder
	r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP))
	bashPPRun(t, r, src)
	qt.Assert(t, qt.Equals(out.String(), "3"))
}

func TestBashPPNamedResultReturnConstRemainsMutableToDefer(t *testing.T) {
	const src = `const source int = 5
func result() (n int) {
 defer func() {
  n=6
 }()
 return source
}
func main() {
 n := result()
 printf '%s' "$n"
}
main()
`
	var out strings.Builder
	r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP))
	bashPPRun(t, r, src)
	qt.Assert(t, qt.Equals(out.String(), "6"))
}

func TestBashPPInitFunctionIsExplicitlyUnsupported(t *testing.T) {
	const src = "func init() {\n}\n"
	var out strings.Builder
	r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP), interp.WithBashCompatErrors(true))
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "init.bpp")
	qt.Assert(t, qt.IsNil(err))
	err = r.Run(context.Background(), file)
	qt.Assert(t, qt.ErrorIs(err, interp.ExitStatus(2)))
	qt.Assert(t, qt.StringContains(out.String(), "init.bpp: line 1: BASHPP-EINIT-FUNC:"))
}
