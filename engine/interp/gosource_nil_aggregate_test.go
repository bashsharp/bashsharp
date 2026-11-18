//go:build full

package interp_test

// Sprint: #118; Story: #56; Story-ID: 3ef468f4e831
//
// Aggregate-element coverage for the nil repair in interp/gosource_nil.go
// (goSourceNilElement / goSourceInterfaceElement) and its two call sites in
// bashPPEvalElement.
//
// Aggregate storage carries (value, meta) rather than a cell, so a nil written
// into a table entry, a struct field or a map value never reaches the
// variable/argument paths that already knew how to build one. Before the
// repair those elements fell through to the scalar path and reported
// BASHPP-EEXPR-NIL, or -- worse for an interface element -- would have had to
// coerce nil to "" to get past it.
//
// Every case here is an authored program, not a forwarded standard-library
// body. Each runs through all three controls: native Go as the oracle, the
// interpreter over the original AST, and the lowered artifact built from
// source that is deleted before it runs.

import "testing"

// TestGoSourceNilAggregateThreeModes pins that an aggregate element keeps the
// dynamic identity of the nil it was given. The distinction Go draws -- an
// untyped nil leaves an interface nil, a typed nil conversion stores a
// non-nil interface holding a nil pointer -- has to survive the trip through
// element storage, which is what goSourceNilElement's interface branch and
// goSourceInterfaceElement's boxing exist for.
func TestGoSourceNilAggregateThreeModes(t *testing.T) {
	cases := map[string]string{
		// The typed half of the initializer shape wrap_test.go's
		// TestAsValidation writes: an []any holding a typed nil pointer, a
		// string and a live pointer. A typed nil conversion must stay a
		// non-nil interface holding a nil *int, and the string element must
		// keep its dynamic type instead of being rejected as a bare scalar,
		// which is goSourceInterfaceElement's boxing. Authored here rather
		// than forwarded.
		//
		// The untyped nil entry the original also carries is pinned by
		// untyped_nil_interface_element_identity below, which was held in
		// gosource_collection_gate_test.go until the read-back repair landed.
		"interface_table_typed_nil_and_dynamic": `package main

import "fmt"

func main() {
	var s string
	table := []any{(*int)(nil), "error", &s}
	for _, tc := range table {
		fmt.Printf("%T|%t\n", tc, tc == nil)
	}
	fmt.Printf("%v|%v\n", table[0], table[1])
}
`,
		// goSourceNilableType decides which field shapes may take an untyped
		// nil at all. These are the non-interface kinds whose Go zero value
		// is nil, so they take goSourceNilElement's zero-value branch: a
		// regression that narrowed that set turns them into refusals, and one
		// that widened it lets a scalar field swallow nil as "".
		"nilable_field_kinds": `package main

import "fmt"

type box struct {
	p *int
	s []int
	m map[string]int
}

func main() {
	b := box{nil, nil, nil}
	fmt.Println(b.p == nil, b.s == nil, b.m == nil)
	fmt.Println(len(b.s), len(b.m))
	b.s = append(b.s, 1)
	fmt.Println(b.s, len(b.s))
}
`,
		// Nested aggregates re-enter the element path for the inner literal,
		// so a typed nil two levels down must survive as well.
		"nested_aggregate_typed_nil": `package main

import "fmt"

type inner struct{ target any }

type outer struct {
	rows []inner
}

func main() {
	o := outer{rows: []inner{{target: (*int)(nil)}, {target: "s"}}}
	fmt.Printf("%T|%t|%T|%t\n", o.rows[0].target, o.rows[0].target == nil, o.rows[1].target, o.rows[1].target == nil)
	fmt.Println(len(o.rows))
	tables := [][]any{{(*int)(nil)}, {"s"}}
	fmt.Printf("%T|%v\n", tables[0][0], tables[1][0])
}
`,
		// Promoted unchanged from TestGoSourceNilAggregateGate once the
		// element read-back repair landed. goSourceNilElement always built
		// the untyped nil correctly on the way in -- bashPPMakeInterfaceValue
		// returns nilIface for a bare nil and the element meta carries it --
		// but the identity was dropped on the way back out: an element read
		// from a slice or a map arrives as the printable payload, not as the
		// *bashPPInterfaceValue a variable of the same type carries, so
		// comparing it to nil answered false where Go answers true and an
		// untyped nil interface element was indistinguishable from a typed
		// nil one. bashPPComparablePayload recovers it from the meta. The
		// plain `var v any = nil` line was never affected and stays here as
		// the in-program control that the variable path is unchanged.
		"untyped_nil_interface_element_identity": `package main

import "fmt"

func main() {
	table := []any{nil}
	fmt.Printf("%T|%t\n", table[0], table[0] == nil)
	var v any = nil
	fmt.Println(v == nil)
	m := map[string]any{"a": nil}
	fmt.Println(m["a"] == nil)
}
`,
		// Promoted unchanged from TestGoSourceNilAggregateGate. The zero-value
		// branch built the nil pointer element, but `(*T)(nil)` is a
		// BashPPConvertExpr rather than the bare nil ident the pointer paths
		// recognise, so the literal could not even be constructed: it reached
		// the generic structured read-back and reported BASHPP-ESELECTOR-EXPR.
		// bashPPNilPointerConversion answers it as a nil pointer of the
		// conversion's own type, which keeps the assignability check honest
		// while letting both element spellings range and compare.
		"nil_pointer_slice_element_range": `package main

import "fmt"

type T struct{ N int }

func main() {
	ps := []*T{nil, (*T)(nil)}
	for _, p := range ps {
		fmt.Println(p == nil)
	}
	fmt.Println(len(ps))
}
`,
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) { typedSendThreeModes(t, source) })
	}
}
