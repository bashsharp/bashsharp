//go:build full

package interp_test

// Sprint: #248; Story: #702; Story-ID: f330582c10c8
//
// The session-fixed native type facts, the local predeclared-scalar
// comparison and the indexed transport origins change cost, not answers.
// Each program runs unchanged against a real Go build and must match its
// stdout, stderr and exit status exactly.

import "testing"

// issue9604b's generator shape, reduced: new(big.Int) validated in a loop,
// *big.Int slices grown by append, native uint/int results compared with
// untyped constants, and interpreter pointers handed to big.Int methods
// repeatedly (every trunc registers fresh transport origins).
func TestS248NativeFactsBigIntGenerator(t *testing.T) {
	differGoSource(t, `package main

import (
	"fmt"
	"math/big"
)

var one = big.NewInt(1)

func values(bits uint, signed bool) []*big.Int {
	var a []*big.Int
	a = append(a, big.NewInt(0), big.NewInt(1), big.NewInt(2))
	r := big.NewInt(1)
	if signed {
		a = append(a, big.NewInt(-1), r.Lsh(r, bits-1).Neg(r))
	} else {
		a = append(a, r.Lsh(r, bits).Sub(r, one))
	}
	return a
}

func trunc(x *big.Int, bits uint, signed bool) *big.Int {
	r := new(big.Int)
	m := new(big.Int)
	m.Lsh(one, bits)
	m.Sub(m, one)
	r.And(x, m)
	if signed && r.Bit(int(bits)-1) == 1 {
		m.Neg(one)
		m.Lsh(m, bits)
		r.Or(r, m)
	}
	return r
}

func main() {
	count, zeros, ones := 0, 0, 0
	for _, bits := range []uint{8, 16, 64} {
		for _, signed := range []bool{false, true} {
			for _, x := range values(bits, signed) {
				for _, y := range values(bits, signed) {
					if y.Sign() == 0 {
						zeros++
						continue
					}
					r := trunc(new(big.Int).Mul(x, y), bits, signed)
					if r.Bit(0) != 0 {
						ones++
					}
					if r.Sign() != 0 && r.Cmp(x) == 0 {
						count++
					}
					fmt.Printf("%d*%d=%d ", x, y, r)
				}
			}
			fmt.Println()
		}
	}
	fmt.Println(count, zeros, ones)
}
`, nil, "")
}

// Predeclared-scalar comparisons of native results at the edges of their
// types, beside comparisons the local path must decline (named types,
// floats, strings, interfaces) and leave to the dependency.
func TestS248NativeScalarComparisons(t *testing.T) {
	differGoSource(t, `package main

import (
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
	"time"
)

func main() {
	x := big.NewInt(-128)
	fmt.Println(x.Int64() == -128, x.Int64() != -128, x.Sign() == -1, x.Sign() == 1)
	u := new(big.Int).SetUint64(math.MaxUint64)
	fmt.Println(u.Uint64() == math.MaxUint64, u.Uint64() == 0, u.BitLen() == 64, u.Bit(63) == 1)
	n, err := strconv.ParseInt("-9223372036854775808", 10, 64)
	fmt.Println(n == math.MinInt64, err == nil)
	i8, _ := strconv.ParseInt("127", 10, 8)
	fmt.Println(int8(i8) == math.MaxInt8, i8 == 127)
	b := strings.HasPrefix("abc", "a")
	fmt.Println(b == true, b == false, strings.Contains("abc", "d") == false)
	d := time.Duration(1500) * time.Millisecond
	fmt.Println(d == 1500*time.Millisecond, time.March == time.Month(3), d.Seconds() == 1.5)
	f := math.NaN()
	fmt.Println(math.Sqrt(f) == math.Sqrt(f), math.Copysign(0, -1) == 0)
	fmt.Println(strconv.Itoa(7) == "7", fmt.Sprint(3) == "3")
	var e error
	_, e = strconv.Atoi("x")
	fmt.Println(e == nil, e != nil)
}
`, nil, "")
}
