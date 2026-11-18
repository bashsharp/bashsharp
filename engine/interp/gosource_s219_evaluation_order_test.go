//go:build full

package interp_test

// Sprint: #219; Story: #461; Story-ID: 4ed649697945

import "testing"

// TestS219EvaluationOrder pins Go's two-phase assignment rule and the
// left-to-right evaluation of expression operands. Calls on the right-hand
// side must run once in source order, and none of the assignment writes may
// become visible until every right-hand value has been captured.
func TestS219EvaluationOrder(t *testing.T) {
	typedSendThreeModes(t, `package main

var trace string

func left() bool { trace += "x"; return false }
func right() string { trace += "y"; return "abc" }

func main() {
	if left() == (right() == "abc") { panic("wrong comparison") }
	if trace != "xy" { panic("wrong operand order: " + trace) }

	x := 1
	mutate := func() int { trace += "m"; x = 3; return 2 }
	a, b, c := len(make([]int, x)), mutate(), len(make([]int, x))
	if a != 1 || b != 2 || c != 3 { panic("RHS calls not ordered") }

	s := make([]int, 0)
	mutateSlice := func() int { s = make([]int, 2); return 2 }
	before, middle, after := append(s, 1), mutateSlice(), append(s, 1)
	if len(before) != 1 || middle != 2 || len(after) != 3 { panic("append inputs not captured in order") }

	z := 1.0
	mutateNumber := func() int { z = 3; return 2 }
	realBefore, changed, realAfter := real(complex(z, 0)), mutateNumber(), imag(complex(0, z))
	if realBefore != 1 || changed != 2 || realAfter != 3 { panic("numeric builtins not ordered") }

	dst := make([]int, 10)
	src := make([]int, 1)
	mutateCopy := func() int { src = make([]int, 3); return 2 }
	copiedBefore, copyMiddle, copiedAfter := copy(dst, src), mutateCopy(), copy(dst, src)
	if copiedBefore != 1 || copyMiddle != 2 || copiedAfter != 3 { panic("copy inputs not captured in order") }

	p, q := 10, 20
	p, q = q, p
	if p != 20 || q != 10 { panic("assignment wrote before RHS capture") }

	i := 0
	next := func() int { i++; return i }
	u, v := next(), next()
	if u != 1 || v != 2 || i != 2 { panic("RHS evaluated more than once or out of order") }
	println(trace, a, b, c, p, q, u, v, len(before), len(after), realBefore, realAfter, copiedBefore, copiedAfter)
}
`)
}

func TestS219EvaluationOrderNonFinite(t *testing.T) {
	source := `package main
func main() {
 var zero float64
 inf := 1 / zero
 println((inf + 1) == inf, (inf - inf) == (inf - inf))
}`
	_, stderr, err := runGoSource(t, "s219compareieee", source)
	if err != nil || stderr != "true false\n" {
		t.Fatalf("run=%v stderr=%q", err, stderr)
	}
}
