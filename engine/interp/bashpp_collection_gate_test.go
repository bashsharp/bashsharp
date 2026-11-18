//go:build gosource_collection_gate

package interp_test

// Sprint: #118; Story: #1; Story-ID: 2daf9ef04ad4
//
// Measured-but-unfixed collection cases, kept runnable rather than deleted.
//
// Every case here is a real Go program whose interpreted result differs from
// the native one as of this commit. They are held behind a tag, exactly as
// gosource_testing_corpus_test.go holds its corpus gate, so the difference
// stays reproducible on demand without asserting a fix this slice did not
// make. None of them is skipped, capped or normalised: each runs through the
// same differGoSource oracle as the default suite, so the day the underlying
// surface lands they turn green by themselves.
//
// Run them with:
//
//	go test -tags gosource_collection_gate ./interp -run GoSourceCollectionGate
//
// The measured differences and their owners are recorded in
// docs/plan-gosource-collections.md.

import "testing"

const goSourceCollectionGate = true

func TestGoSourceCollectionGate(t *testing.T) {
	cases := map[string]string{
		"append_string_spread_to_byte_slice": `package main

import "fmt"

func main() {
	bs := []byte("ab")
	bs = append(bs, "cd"...)
	fmt.Println(bs, len(bs), cap(bs), string(bs))
	bs = append(bs, []byte("ef")...)
	fmt.Println(string(bs), len(bs))
}
`,
		"conversion_passed_to_a_call": `package main

import (
	"bytes"
	"fmt"
)

func count(bs []byte) int { return len(bs) }

func main() {
	s := "hello"
	fmt.Println(count([]byte(s)))
	fmt.Println(bytes.Contains([]byte(s), []byte("ell")))
	fmt.Println(string(bytes.ToUpper([]byte(s))))
}
`,
		"copy_does_not_alias": `package main

import "fmt"

func main() {
	src := []int{1, 2, 3}
	dst := make([]int, 2)
	n := copy(dst, src)
	dst[0] = 9
	fmt.Println(n, src, dst)
	fmt.Println(copy(src, src[1:]), src)
}
`,
		"growth_capacity_by_element_type": `package main

import "fmt"

func main() {
	var ints []int
	var strs []string
	var bools []bool
	for i := 0; i < 12; i++ {
		ints = append(ints, i)
		strs = append(strs, "x")
		bools = append(bools, true)
		fmt.Println(i, len(ints), cap(ints), len(strs), cap(strs), len(bools), cap(bools))
	}
}
`,
		"named_element_slice_conversion": `package main

import "fmt"

type Byte = byte

func main() {
	bs := []Byte("hi")
	fmt.Println(bs)
	fmt.Println(string(bs))
	var u8 []uint8 = []uint8("hi")
	fmt.Println(u8, string(u8))
	var i32 []int32 = []int32("hi")
	fmt.Println(i32, string(i32))
}
`, "append_past_capacity_detaches": `package main

import "fmt"

func main() {
	a := []int{1, 2}
	b := append(a, 3)
	b[0] = 100
	fmt.Println(a, b)
	fmt.Println(len(a), cap(a), len(b), cap(b))
}
`,
		"nil_map_reads": `package main

import "fmt"

func main() {
	var m map[string]int
	fmt.Println(m, len(m), m == nil)
	fmt.Println(m["missing"])
	v, ok := m["missing"]
	fmt.Println(v, ok)
	for k := range m {
		fmt.Println(k)
	}
}
`,
		"nil_map_write_panics": `package main

import "fmt"

func main() {
	var m map[string]int
	fmt.Println("before")
	m["k"] = 1
}
`,
		"nil_slice_reslices_empty": `package main

import "fmt"

func main() {
	var s []string
	fmt.Println(s[:0], len(s[:0]), s[:0] == nil)
	fmt.Println(append(s[:0], "a"))
	var t []int
	u := t[0:0:0]
	fmt.Println(u, u == nil, len(u), cap(u))
}
`, "nil_slice_index_panics": `package main

import "fmt"

func main() {
	var s []int
	fmt.Println("before")
	fmt.Println(s[0])
}
`,
		"spare_capacity_is_zeroed": `package main

import "fmt"

func main() {
	b := make([]int, 0, 5)
	fmt.Println(b, len(b), cap(b))
	fmt.Println(b[:5], b[:3], b[2:4])
	s := make([]string, 1, 4)
	fmt.Printf("%q %q\n", s, s[:4])
	type point struct{ X, Y int }
	p := make([]point, 0, 3)
	fmt.Println(p[:3])
}
`,
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) { differGoSource(t, source, nil, "") })
	}
}

// Struct elements held inside a collection. Every row below reaches, and stops
// at, a surface outside this slice: printing a struct or a slice-of-struct to a
// dependency needs bridge type registration (bridge31 —
// `unregistered bridge type "bag"` / `"[]point"` / `"[2]point"`), and
// `rows[i] = make(...)` / `m["a"] = append(...)` need an indexed assignment
// target to accept a predeclared value call. Measured, not asserted.
// TestGoSourceCollectionGateStructElements covers structured elements held inside a
// collection. A struct element of a slice is addressable and is mutated in
// place, while a struct element of a map is not; a slice of slices shares its
// inner rows; and a struct field that is itself a slice aliases whatever was
// assigned into it.
func TestGoSourceCollectionGateStructElements(t *testing.T) {
	cases := map[string]string{
		"slice_struct_element_is_addressable": `package main

import "fmt"

type point struct{ X, Y int }

func main() {
	ps := []point{{1, 2}, {3, 4}}
	ps[0].X = 10
	fmt.Println(ps)
	p := &ps[1]
	p.Y = 40
	fmt.Println(ps, ps[1].Y)
	q := ps[1]
	q.X = 99
	fmt.Println(ps[1], q)
}
`,
		"struct_field_slice_aliases": `package main

import "fmt"

type bag struct {
	Items []int
	Name  string
}

func main() {
	items := []int{1, 2, 3}
	b := bag{Items: items, Name: "b"}
	b.Items[0] = 9
	fmt.Println(items, b)
	b.Items = append(b.Items, 4)
	b.Items[1] = 8
	fmt.Println(items, b.Items, len(b.Items), cap(b.Items))
}
`,
		"struct_value_copy_shares_slice_field": `package main

import "fmt"

type bag struct{ Items []int }

func main() {
	a := bag{Items: []int{1, 2}}
	c := a
	c.Items[0] = 7
	fmt.Println(a, c)
	c.Items = append(c.Items, 3)
	c.Items[1] = 8
	fmt.Println(a, c)
}
`,
		"slice_of_slices_shares_rows": `package main

import "fmt"

func main() {
	rows := make([][]int, 2)
	for i := range rows {
		rows[i] = make([]int, 3)
	}
	row := rows[0]
	row[1] = 5
	fmt.Println(rows, rows[0][1])
	rows[1][2] = 6
	fmt.Println(rows, len(rows), cap(rows))
	fmt.Println(append(rows, []int{7}))
}
`,
		"map_of_slices_grows": `package main

import (
	"fmt"
	"sort"
)

type entry struct {
	Key  string
	Vals []int
}

func main() {
	m := map[string][]int{}
	m["a"] = append(m["a"], 1)
	m["a"] = append(m["a"], 2)
	m["b"] = append(m["b"], 3)
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Println(k, m[k], len(m[k]), cap(m[k]))
	}
	e := entry{Key: "a", Vals: m["a"]}
	e.Vals[0] = 100
	fmt.Println(e, m["a"])
}
`,
		"array_of_structs_copies_on_assignment": `package main

import "fmt"

type point struct{ X, Y int }

func main() {
	a := [2]point{{1, 2}, {3, 4}}
	b := a
	b[0].X = 10
	fmt.Println(a, b)
	s := a[:]
	s[0].Y = 20
	fmt.Println(a, s)
}
`,
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) { differGoSource(t, source, nil, "") })
	}
}
