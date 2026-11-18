//go:build full

package interp_test

// Sprint: #219; Story: #460; Story-ID: d8e7d58f362b

import (
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/go-quicktest/qt"
)

func TestS219MapStorage(t *testing.T) {
	src := `package main
import (
	"fmt"
	"math"
)
type item struct { n int }
type outer struct { *item }
func main() {
	nan := math.NaN()
	m := map[float64]int{}
	for i := 0; i < 3; i++ { m[nan] = i }
	_, found := m[nan]
	m[4] = 1
	m[4] = 9
	fmt.Println(len(m), found, m[4])

	// Ordinary comparable-array control only. The optional native comparable
	// array-key path from issue65957 remains a separate residual.
	a := map[[4]int32]int{}
	k := [4]int32{0, 1, 2, 3}
	a[k] = 7
	fmt.Println(a[[4]int32{0, 1, 2, 3}])

	p := &item{n: 10}
	pm := map[int]*item{1: p}
	keyCalls := 0
	key := func() int { keyCalls++; return 1 }
	pm[key()].n++
	fmt.Println(pm[1].n, p.n, keyCalls)

	s := []item{{n: 20}}
	indexCalls := 0
	next := func() int { indexCalls++; return 0 }
	s[next()].n++
	fmt.Println(s[0].n, indexCalls)

	embedded := &item{n: 30}
	om := map[int]*outer{1: {item: embedded}}
	om[key()].n++
	fmt.Println(om[1].n, embedded.n, keyCalls)
}`
	out, stderr, err := runGoSource(t, "s219_map_storage", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "4 false 9\n7\n11 11 1\n21 1\n31 31 2\n"))

	nonPointer := `package main
type item struct { n int }
func main() { m := map[int]item{1: {n: 2}}; _ = &m[1].n }`
	_, err = gosource.Parse(strings.NewReader(nonPointer), "s219_map_struct_address.go", gosource.Options{RunMain: true})
	qt.Assert(t, qt.IsNotNil(err))
	qt.Assert(t, qt.StringContains(err.Error(), "cannot take address"))
}
