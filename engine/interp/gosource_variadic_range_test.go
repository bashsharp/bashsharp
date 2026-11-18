// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

//go:build full

package interp_test

// Sprint: #118; Story: #3; Story-ID: fa07603b71dc
//
// bashPPInvoke binds a variadic parameter as an indexed shell variable, with
// no collection metadata attached, so a two-variable `for i, v := range`
// over it fell back to the scalar range path and iterated len(args) times
// with v left unset instead of walking the arguments themselves. Both cases
// below run the unchanged original source and compare stdout, stderr and
// exit status against a real Go build of the same file, so what is pinned is
// that a two-variable range over a variadic parameter behaves exactly as Go
// requires, not an interpreter-only expectation.
import (
	"testing"

	"github.com/go-quicktest/qt"
)

func TestGoSourceVariadicTwoVariableRange(t *testing.T) {
	cases := map[string]string{
		"multiple_arguments": `package main

import "fmt"

func sum(nums ...int) int {
	total := 0
	for i, v := range nums {
		fmt.Println(i, v)
		total += v
	}
	return total
}

func main() {
	fmt.Println(sum(10, 20, 30))
}
`,
		"zero_arguments": `package main

import "fmt"

func sum(nums ...int) int {
	total := 0
	for i, v := range nums {
		fmt.Println(i, v)
		total += v
	}
	return total
}

func main() {
	fmt.Println(sum())
}
`,
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			differGoSource(t, source, nil, "")
		})
	}
}

// Sprint: #209; Story: #461; Story-ID: 4ed649697945
//
// The Go corpus's fixedbugs/bug473.go first exposes this through global
// initializers. Keep this compact program outside that corpus so it pins only
// the boundary at issue: an interface{} entering a variadic slice must still
// be an interface after its range iteration. A plain []interface{} range is
// the deliberately matching control case, while the final call proves that a
// real dynamic-type mismatch still panics.
func TestGoSourceVariadicInterfaceRangeCarrier(t *testing.T) {
	const source = `package main

import "fmt"

func sumVariadic(xs ...interface{}) int {
	total := 0
	for _, x := range xs {
		total += x.(int)
	}
	return total
}

func sumSlice(xs []interface{}) int {
	total := 0
	for _, x := range xs {
		total += x.(int)
	}
	return total
}

func mismatch(xs ...interface{}) (caught bool) {
	defer func() { caught = recover() != nil }()
	for _, x := range xs {
		_ = x.(int)
	}
	return false
}

func main() {
	fmt.Println(sumVariadic(1, 2, 3))
	fmt.Println(sumSlice([]interface{}{1, 2, 3}))
	fmt.Println(mismatch(1, "not an int"))
}
`
	out, stderr, err := runGoSource(t, "story461_variadic_interface_range", source)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "6\n6\ntrue\n"))
}
