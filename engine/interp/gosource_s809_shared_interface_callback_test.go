//go:build full

package interp_test

// Sprint: #281; Story: #809; Story-ID: fac7e14af4a8

import "testing"

// TestS809SharedInterfaceGenericCallbacks runs the same source interpreted and
// natively. The generic helpers must receive the original interface values:
// pointer identity, pointee mutations, and the sorted backing array are all
// observable after each call.
func TestS809SharedInterfaceGenericCallbacks(t *testing.T) {
	const source = `package main
import (
	"cmp"
	"fmt"
	"slices"
)
type I interface { order() uint32; touch() }
type alpha struct { n uint32; seen int }
type beta struct { n uint32; seen int }
func (x *alpha) order() uint32 { return x.n }
func (x *alpha) touch() { x.seen++ }
func (x *beta) order() uint32 { return x.n }
func (x *beta) touch() { x.seen++ }
func orders(xs []I) {
	for _, x := range xs { fmt.Printf("%d ", x.order()) }
	fmt.Println()
}
func main() {
	a, b, c, d := &alpha{n: 30}, &beta{n: 10}, &alpha{n: 20}, &beta{n: 20}
	list := []I{a, b, c, d}
	slices.SortFunc(list, func(x, y I) int {
		x.touch()
		return cmp.Compare(x.order(), y.order())
	})
	orders(list)
	fmt.Println(list[0] == b, list[1] == c || list[1] == d, a.seen+b.seen+c.seen+d.seen > 0)

	stable := []I{a, c, d, b}
	slices.SortStableFunc(stable, func(x, y I) int { return cmp.Compare(x.order(), y.order()) })
	orders(stable)
	fmt.Println(stable[1] == c, stable[2] == d)

	fmt.Println(slices.IndexFunc(stable, func(x I) bool { x.touch(); return x == d }))
	fmt.Println(slices.ContainsFunc(stable, func(x I) bool { return x == a }), d.seen > 0)
}`
	differGoSource(t, source, nil, "")
}
