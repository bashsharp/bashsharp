// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

//go:build full

package interp_test

// Sprint: #209; Story: #462; Story-ID: e348d2c13248
//
// These outside-corpus programs cover the generic-method call-target seam
// behind all six Barrier D keys: interpreted and compiled modes for
// fixedbugs/issue80976.go, genmeth1.go, and genmeth2.go.

import "testing"

func TestGoSourceInstantiatedMethodCallTargetsThreeModes(t *testing.T) {
	for name, source := range map[string]string{
		// Go 1.27.1 fixedbugs/issue80976.go: the inferred method type
		// argument refers to an instantiation whose spelling ends in the
		// method name.
		"inferred_argument_ending_in_method_name": `package main

type TypeMap struct{}

func (tm *TypeMap) Set[T any](v T) {}

type HashSet[T any] struct{}

type result struct{ set HashSet[int] }

func main() { (&TypeMap{}).Set(result{}) }
`,
		// One program exercises the remaining call-target forms in genmeth1
		// and genmeth2: explicit type arguments on value and pointer method
		// calls, a method value, and a method expression.
		"explicit_value_pointer_and_method_values": `package main

type Box[T any] struct{ value T }

func (b Box[T]) Pair[U any](u U) (T, U) { return b.value, u }

type Plain struct{}

func (p *Plain) Identity[T any](v T) T { return v }

func main() {
	a, b := Box[int]{value: 7}.Pair[string]("direct")
	println(a, b)
	println((&Plain{}).Identity[int](9))

	value := Box[int]{value: 8}.Pair[string]
	a, b = value("value")
	println(a, b)

	expression := Box[int].Pair[string]
	a, b = expression(Box[int]{value: 10}, "expression")
	println(a, b)
}
`,
	} {
		t.Run(name, func(t *testing.T) {
			typedSendThreeModes(t, source)
		})
	}
}
