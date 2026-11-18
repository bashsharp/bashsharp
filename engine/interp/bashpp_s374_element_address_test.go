//go:build full

package interp_test

// Sprint: #374; Story-ID: 32212c532e3d
//
// The address of an array element is the array's address plus the element's
// offset. A pointer to an element (or to a field inside one) that crosses to
// a dependency must therefore land inside ONE dependency-side array, not in
// an allocation of its own: fmt's %p of &a[1] is %p of &a[0] plus the element
// size (test/fixedbugs/bug260.go). Every case prints only strides and
// equalities, never an address, and runs against a real Go build.
import "testing"

func TestGoSourceNativeElementPointerAddresses(t *testing.T) {
	const prelude = `package main

import (
	"fmt"
	"strconv"
)

func addr(p any) uint64 {
	n, err := strconv.ParseUint(fmt.Sprintf("%p", p), 0, 64)
	if err != nil {
		panic(err)
	}
	return n
}

var _ = addr
`
	cases := map[string]string{
		"strides_follow_element_size": prelude + `
type T1 struct{ x uint8 }
type T2 struct{ x uint16 }
type T4 struct{ x uint32 }
type padded struct {
	a uint8
	b uint64
	c uint16
}

func main() {
	var b1 [10]T1
	var b2 [10]T2
	var b4 [10]T4
	var bp [4]padded
	var u8 [3]uint8
	var u16 [3]uint16
	var u32 [3]uint32
	fmt.Println("T1", addr(&b1[1])-addr(&b1[0]), addr(&b1[2])-addr(&b1[1]))
	fmt.Println("T2", addr(&b2[1])-addr(&b2[0]), addr(&b2[2])-addr(&b2[1]))
	fmt.Println("T4", addr(&b4[1])-addr(&b4[0]), addr(&b4[2])-addr(&b4[1]))
	fmt.Println("padded", addr(&bp[1])-addr(&bp[0]), addr(&bp[2])-addr(&bp[1]))
	fmt.Println("uint8", addr(&u8[1])-addr(&u8[0]), addr(&u8[2])-addr(&u8[1]))
	fmt.Println("uint16", addr(&u16[1])-addr(&u16[0]), addr(&u16[2])-addr(&u16[1]))
	fmt.Println("uint32", addr(&u32[1])-addr(&u32[0]), addr(&u32[2])-addr(&u32[1]))
	// Out of order, and again: an element's address never moves.
	last, first := addr(&bp[3]), addr(&bp[0])
	fmt.Println("span", last-first, addr(&bp[3]) == last, addr(&bp[0]) == first)
}
`,
		"same_element_same_address": prelude + `
type T struct{ x uint32 }

func main() {
	var a [3]T
	p := &a[1]
	one := fmt.Sprintf("%p", p)
	a[1].x = 9
	two := fmt.Sprintf("%p", &a[1])
	three := fmt.Sprintf("%p", p)
	fmt.Println(one == two, two == three, one != fmt.Sprintf("%p", &a[2]))
	fmt.Println(*p, a)
}
`,
		"nested_arrays_and_fields": prelude + `
type inner struct {
	a uint8
	b uint32
	c [3]uint16
}
type outer struct {
	tag  uint64
	rows [2][3]inner
}

func main() {
	var grid [2][3]uint32
	fmt.Println("grid", addr(&grid[0][1])-addr(&grid[0][0]), addr(&grid[1][0])-addr(&grid[0][0]), addr(&grid[1][2])-addr(&grid[0][0]))
	fmt.Println("row", addr(&grid[1])-addr(&grid[0]), addr(&grid[0]) == addr(&grid[0][0]))
	var items [3]inner
	fmt.Println("field", addr(&items[0].b)-addr(&items[0].a), addr(&items[1].b)-addr(&items[0].b), addr(&items[2].a)-addr(&items[0].a))
	fmt.Println("deep", addr(&items[1].c[2])-addr(&items[1].c[0]), addr(&items[1].c[0])-addr(&items[1].a), addr(&items[0]) == addr(&items[0].a))
	var o outer
	fmt.Println("struct", addr(&o.rows[1][2].b)-addr(&o.rows[0][0].b), addr(&o.rows[0][1])-addr(&o.rows[0][0]))
	p := &o
	fmt.Println("through", addr(&p.rows[1][0].c[1])-addr(&p.rows[0][0].c[1]))
}
`,
		"write_through_element_only": `package main

import "fmt"

type pair struct {
	n int
	s string
}

func main() {
	a := [3]int{10, 20, 30}
	n, err := fmt.Sscan("77", &a[1])
	fmt.Println(n, err, a)
	a[0] = 11
	n, err = fmt.Sscan("88", &a[2])
	fmt.Println(n, err, a)
	n, err = fmt.Sscan("5 6", &a[0], &a[1])
	fmt.Println(n, err, a)

	ps := [3]pair{{1, "a"}, {2, "b"}, {3, "c"}}
	n, err = fmt.Sscan("42 word", &ps[1].n, &ps[2].s)
	fmt.Println(n, err, ps)
	ps[1].s = "kept"
	n, err = fmt.Sscan("43", &ps[1].n)
	fmt.Println(n, err, ps)

	var grid [2][2]uint16
	n, err = fmt.Sscan("7 9", &grid[1][0], &grid[0][1])
	fmt.Println(n, err, grid)
	fmt.Println(fmt.Sprintf("%p", &grid[1][0]) == fmt.Sprintf("%p", &grid[1][0]), grid)
}
`,
		"zero_size_and_whole_array": prelude + `
type empty struct{}

func main() {
	var z [4]empty
	fmt.Println("zero", addr(&z[3])-addr(&z[0]))
	a := [3]uint32{1, 2, 3}
	whole := &a
	fmt.Println(*whole, fmt.Sprintf("%p", whole) == fmt.Sprintf("%p", &a))
	fmt.Println(addr(&a[2])-addr(&a[0]), a)
	n, err := fmt.Sscan("8", &whole[1])
	fmt.Println(n, err, a, *whole)
}
`,
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) { differGoSource(t, source, nil, "") })
	}
}
