//go:build full

package interp_test

// Sprint: #248; Story: #702; Story-ID: f330582c10c8
//
// Wrapping integer arithmetic and named-type resolution in the loop shapes
// of copy.go and issue20780b.go, against a real Go build: every width and
// signedness overflows, named slice/array types (and an alias of an array
// with a float-constant length) resolve through chains, and the results
// print exactly as Go prints them.

import "testing"

func TestS248ScalarLoopWrapAndNamedTypes(t *testing.T) {
	differGoSource(t, `package main

import "fmt"

const N = 2e2

type Big = [N]int
type my8 []uint8
type mine my8
type my16 []uint16

func u16(ii int) uint16 {
	var i = uint16(ii)
	i = 'a' + i%26
	i |= i << 8
	return i
}

func fill(k int) (x Big) {
	for i := range x {
		x[i] = k*N + i
	}
	return
}

func main() {
	var i8 int8 = 120
	var u8 uint8 = 250
	var i16 int16 = 32760
	var u32 uint32 = 4294967290
	var i64 int64 = 9223372036854775800
	var u64 uint64 = 18446744073709551610
	var r rune = 2147483640
	var up uintptr = 3
	for k := 0; k < 12; k++ {
		i8 += 3
		u8 += 3
		i16 += 3
		u32 += 3
		i64 += 3
		u64 += 3
		r += 3
		up -= 1
		fmt.Println(i8, u8, i16, u32, i64, u64, r, up, i8*i8, u8*u8, -i8, ^u8, i16<<3, u32>>1)
	}
	x := fill(3)
	s := 0
	for i := range x {
		if x[i] != 3*N+i {
			panic("bad fill")
		}
		s += x[i]
	}
	out := make([]uint8, 10)
	in := []uint8("abcdefghij")
	n := copy(mine(out[2:7]), my8(in[4:]))
	w := make(my16, 4)
	for i := range w {
		w[i] = u16(i + 13)
	}
	fmt.Println(s, n, string(out), w, len(x))
}
`, nil, "")
}
