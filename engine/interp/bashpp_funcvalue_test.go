// Copyright (c) 2025, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package interp_test

import (
	"strings"
	"testing"
)

// The three controls the repair is measured against: a closure value, a named
// non-generic function, and a named GENERIC function, each passed to the same
// concrete `func() int` parameter. Real Go accepts all three and prints the
// same thing for each, so the interpreter has to as well.
func TestBashPPContextualFuncValueControls(t *testing.T) {
	cases := []struct{ name, source, want string }{
		{"closure-context-control", `func consume(fn func() int) { println("accepted") }
fn := func() int { return 7 }
consume(fn)
`, "accepted\n"},
		{"named-context-control", `func fixed() int { return 7 }
func consume(fn func() int) { println("accepted") }
consume(fixed)
`, "accepted\n"},
		{"contextual-generic-function-value", `func f2[P any]() P { var x P; return x }
func consume(fn func() int) { println("accepted") }
consume(f2)
`, "accepted\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, stderr, err := runBashSharpCall(t, tc.source)
			if err != nil || stderr != "" || out != tc.want {
				t.Fatalf("out=%q stderr=%q err=%v want=%q", out, stderr, err, tc.want)
			}
		})
	}
}

// Binding is not just acceptance: the value must be CALLABLE, and a generic one
// must run at the type the context inferred — `var x P; return x` yields int's
// zero value, not the empty string a string P would give.
func TestBashPPContextualFuncValueCalls(t *testing.T) {
	cases := []struct{ name, source, want string }{
		{"named", `func fixed() int { return 7 }
func consume(fn func() int) { value := fn(); println(value) }
consume(fixed)
`, "7\n"},
		{"generic-result-inference", `func f2[P any]() P { var x P; return x }
func consume(fn func() int) { value := fn(); println(value) }
consume(f2)
`, "0\n"},
		{"generic-parameter-inference", `func echo[P any](v P) P { return v }
func consume(fn func(int) int) { value := fn(4); println(value) }
consume(echo)
`, "4\n"},
		{"generic-parameter-only", `func sink[P any](v P) { println("got", v) }
func consume(fn func(string)) { fn("x") }
consume(sink)
`, "got x\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, stderr, err := runBashSharpCall(t, tc.source)
			if err != nil || stderr != "" || out != tc.want {
				t.Fatalf("out=%q stderr=%q err=%v want=%q", out, stderr, err, tc.want)
			}
		})
	}
}

// The negative controls. Each one is rejected in real Go too; what is asserted
// here is that the repair did not widen the parameter's type into "any name of
// a function will do".
func TestBashPPContextualFuncValueRejections(t *testing.T) {
	cases := []struct{ name, source, want string }{
		{"result-mismatch", `func fixed() string { return "seven" }
func consume(fn func() int) { println("accepted") }
consume(fixed)
`, "BASHPP-EARG-FUNCTYPE:"},
		{"arity-mismatch", `func two(a int, b int) int { return a }
func consume(fn func() int) { println("accepted") }
consume(two)
`, "BASHPP-EARG-FUNCTYPE:"},
		{"generic-arity-mismatch", `func echo[P any](v P) P { return v }
func consume(fn func() int) { println("accepted") }
consume(echo)
`, "BASHPP-EARG-FUNCTYPE:"},
		{"generic-signature-mismatch", `func pairwise[P any](a P, b P) P { return a }
func consume(fn func(int, string) int) { println("accepted") }
consume(pairwise)
`, "BASHPP-EARG-FUNCTYPE:"},
		{"generic-uninferable", `func hidden[P any]() int { return 0 }
func consume(fn func() int) { println("accepted") }
consume(hidden)
`, "BASHPP-EGENERIC-INFER:"},
		{"generic-constraint-unsatisfied", `type Number interface { ~int | ~float64 }
func zero[P Number]() P { var x P; return x }
func consume(fn func() string) { println("accepted") }
consume(zero)
`, "BASHPP-EGENERIC-CONSTRAINT:"},
		{"undeclared-name", `func consume(fn func() int) { println("accepted") }
consume(missing)
`, "BASHPP-EARG-FUNCTYPE:"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, stderr, err := runBashSharpCall(t, tc.source)
			if err == nil || out != "" || !strings.Contains(stderr, tc.want) {
				t.Fatalf("out=%q stderr=%q err=%v want=%q", out, stderr, err, tc.want)
			}
		})
	}
}
