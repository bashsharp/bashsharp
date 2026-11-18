//go:build full

package interp_test

// Sprint: #118; Story: #56; Story-ID: 3ef468f4e831
//
// Adversarial controls for the nil-aggregate repair reviewed in this run:
// goSourceNilElement / goSourceInterfaceElement (interp/gosource_nil.go),
// bashPPNilPointerConversion on the two pointer-expression paths
// (interp/bashpp_pointer.go), and bashPPComparablePayload's meta read-back
// (interp/bashpp_scalar.go).
//
// TestGoSourceNilAggregateThreeModes pins that a nil written into an
// aggregate is *constructed* correctly and reads back with its identity.
// Every case there builds its aggregate once and only reads it. That leaves
// the repair's riskiest surface untested, because bashPPComparablePayload
// answers from the element meta rather than from the payload: any path that
// rewrites an element's payload without rewriting its meta keeps answering
// from the identity the element used to have, and a construct-and-read test
// cannot see it. The same blind spot covers aliasing (two names over one
// backing store), map assignment and deletion, range's copied loop variable,
// and interface equality between two elements rather than between an element
// and the nil literal -- goSourceInterfaceEqual takes bashPPComparablePayload
// on *both* sides, a call the read-only cases only ever exercise on one.
//
// Each case is an authored program, not a forwarded standard-library body,
// and runs through the same three controls: native Go as the oracle, the
// interpreter over the original AST, and the lowered artifact built from
// source deleted before it runs.

import "testing"

func TestGoSourceNilAggregateAdversarialThreeModes(t *testing.T) {
	cases := map[string]string{
		// Mutation. bashPPComparablePayload recovers an interface from the
		// element meta, so an element whose payload is overwritten in place
		// must have its meta overwritten with it. Each slot here is written
		// at least twice and crosses the nil boundary in both directions:
		// nil -> concrete, concrete -> nil, and typed nil -> untyped nil.
		// A stale meta answers with the element's previous identity and this
		// diverges from the oracle on the very first %t.
		"element_mutation_crosses_nil_both_ways": `package main

import "fmt"

func main() {
	table := []any{nil, "live", (*int)(nil)}
	fmt.Printf("%t %t %t\n", table[0] == nil, table[1] == nil, table[2] == nil)
	table[0] = "now set"
	table[1] = nil
	table[2] = nil
	fmt.Printf("%t %t %t\n", table[0] == nil, table[1] == nil, table[2] == nil)
	fmt.Printf("%T %T %T\n", table[0], table[1], table[2])
	var n *int
	table[1] = n
	fmt.Printf("%t %T\n", table[1] == nil, table[1])
	table[1] = nil
	fmt.Printf("%t %T\n", table[1] == nil, table[1])
}
`,
		// Aliasing. A slice header shares its backing array, so a nil
		// written through one name must be the same nil the other name
		// reads. If element identity were recovered from a meta copied per
		// name rather than carried with the shared element, the two names
		// disagree here while both still look self-consistent.
		"aliased_backing_array_shares_nil_identity": `package main

import "fmt"

func main() {
	table := []any{nil, nil}
	alias := table
	view := table[1:]
	alias[0] = "written through alias"
	view[0] = (*int)(nil)
	fmt.Printf("%t %t\n", table[0] == nil, table[1] == nil)
	fmt.Printf("%v %T\n", table[0], table[1])
	table[1] = nil
	fmt.Printf("%t %t\n", view[0] == nil, alias[1] == nil)
	clear0(table)
	fmt.Printf("%t %t %t\n", table[0] == nil, alias[0] == nil, view[0] == nil)
}

func clear0(s []any) {
	s[0] = nil
}
`,
		// Map. A map value is not addressable, so assignment replaces the
		// whole element rather than mutating it in place, and a missing key
		// yields a fresh zero value that never went through the element
		// path at all. Both must still answer nil the way Go does, and
		// delete must not leave the old identity behind.
		"map_assign_delete_and_missing_key": `package main

import "fmt"

func main() {
	m := map[string]any{"untyped": nil, "typed": (*int)(nil), "live": "v"}
	fmt.Printf("%t %t %t\n", m["untyped"] == nil, m["typed"] == nil, m["live"] == nil)
	fmt.Printf("%t %t\n", m["absent"] == nil, len(m) == 3)
	got, ok := m["untyped"]
	fmt.Printf("%t %t\n", got == nil, ok)
	missing, present := m["absent"]
	fmt.Printf("%t %t\n", missing == nil, present)
	m["untyped"] = (*int)(nil)
	m["typed"] = nil
	fmt.Printf("%t %t\n", m["untyped"] == nil, m["typed"] == nil)
	delete(m, "untyped")
	fmt.Printf("%t %t\n", m["untyped"] == nil, len(m) == 2)
	ptrs := map[string]*int{"a": nil, "b": (*int)(nil)}
	fmt.Printf("%t %t %t\n", ptrs["a"] == nil, ptrs["b"] == nil, ptrs["c"] == nil)
}
`,
		// Range. The loop variable is a copy of the element, so it reaches
		// the comparison through a different value/meta pair than an index
		// read does. Ranging a slice, a map and a nested slice must each
		// agree with the indexed read, and writing back through the index
		// during the range must be what the next iteration sees.
		"range_copy_agrees_with_indexed_read": `package main

import "fmt"

func main() {
	table := []any{nil, (*int)(nil), "s"}
	for i, v := range table {
		fmt.Printf("%d %t %t %T\n", i, v == nil, table[i] == nil, v)
	}
	ptrs := []*int{nil, (*int)(nil)}
	for i, p := range ptrs {
		fmt.Printf("%d %t %t\n", i, p == nil, ptrs[i] == nil)
	}
	nested := [][]any{{nil}, {(*int)(nil)}}
	for _, row := range nested {
		for _, v := range row {
			fmt.Printf("%t %T\n", v == nil, v)
		}
	}
	live := []any{nil, nil}
	for i, v := range live {
		if i == 0 {
			live[1] = "set during range"
		}
		fmt.Printf("%t\n", v == nil)
	}
	fmt.Printf("%t %t\n", live[0] == nil, live[1] == nil)
	one := map[string]any{"k": nil}
	for k, v := range one {
		fmt.Printf("%s %t %t\n", k, v == nil, one[k] == nil)
	}
}
`,
		// Interface equality between two elements. goSourceInterfaceEqual
		// calls bashPPComparablePayload on both operands; every read-only
		// case compares an element against the nil *literal*, which takes
		// the earlier nilLiteral branch and never reaches the two-sided
		// call. Two untyped nils are equal, two typed nils of one type are
		// equal, an untyped nil and a typed nil are not, and two typed nils
		// of different types are not -- the distinction that collapses if
		// both elements read back as the same printable "".
		"element_to_element_interface_equality": `package main

import "fmt"

type A struct{ N int }

type B struct{ N int }

func main() {
	var pa *A
	var pb *B
	table := []any{nil, nil, pa, pa, pb, "s", "s"}
	fmt.Printf("%t %t %t\n", table[0] == table[1], table[2] == table[3], table[0] == table[2])
	fmt.Printf("%t %t\n", table[2] == table[4], table[5] == table[6])
	fmt.Printf("%t %t\n", table[0] == nil, table[2] == nil)
	m := map[string]any{"untyped": nil, "typed": pa}
	fmt.Printf("%t %t\n", m["untyped"] == table[0], m["typed"] == table[2])
	fmt.Printf("%t %t\n", m["untyped"] == m["typed"], m["absent"] == table[1])
	type row struct{ v any }
	rows := []row{{nil}, {pa}}
	fmt.Printf("%t %t %t\n", rows[0].v == table[0], rows[1].v == table[2], rows[0].v == rows[1].v)
}
`,
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) { typedSendThreeModes(t, source) })
	}
}
