//go:build full

package interp_test

import (
	"strings"
	"testing"

	"github.com/go-quicktest/qt"

	"github.com/bashsharp/bashsharp/engine/gosource"
)

// Sprint: #219; Story: #460; Story-ID: d8e7d58f362b
//
// Collection element destinations inside a generic body must use the active
// instantiation. The value itself remains a typed cell: scalar text is not a
// substitute for struct keys, array copies, or pointer/slice field identity.
func TestS219GenericCollectionTransport(t *testing.T) {
	t.Run("blank result zero cells", func(t *testing.T) {
		const src = `package main
import "fmt"
type record struct { n int }
func zeros() (_ int, _ string, _ record, _ *int, _ any) { return }
func main() {
	i, s, v, p, a := zeros()
	fmt.Println(i, len(s), v.n, p == nil, a == nil)
}`
		out, stderr, err := runGoSource(t, "s219blankzeros", src)
		qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
		qt.Assert(t, qt.Equals(stderr, ""))
		qt.Assert(t, qt.Equals(out, "0 0 0 true true\n"))
	})

	t.Run("explicit return into blank result", func(t *testing.T) {
		const src = `package main
import "fmt"
func seven(_ int) (_ int) { return 7 }
func main() { fmt.Println(seven(1)) }
`
		out, stderr, err := runGoSource(t, "s219blankexplicit", src)
		qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
		qt.Assert(t, qt.Equals(stderr, ""))
		qt.Assert(t, qt.Equals(out, "7\n"))
	})

	t.Run("two instantiations and package type shadowing", func(t *testing.T) {
		const src = `package main
import "fmt"
type V int
func zero[T any]() (_ T) { return }
func values[V any]() [1]V { return [...]V{zero[V]()} }
func main() {
	fmt.Println(values[int](), values[string]())
}`
		out, stderr, err := runGoSource(t, "s219genericshadow", src)
		qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
		qt.Assert(t, qt.Equals(stderr, ""))
		qt.Assert(t, qt.Equals(out, "[0] []\n"))
	})

	t.Run("map range preserves comparable struct key", func(t *testing.T) {
		const src = `package main
import "fmt"
type key2[A, B comparable] struct { a A; b B }
func keys[K comparable, V any](m map[K]V) []K {
	r := make([]K, 0, len(m))
	for k := range m { r = append(r, k) }
	return r
}
func main() {
	m := map[key2[int, float64]]int{{1, 2.5}: 9}
	k := keys(m)[0]
	fmt.Println(k.a, k.b, m[k])
}`
		out, stderr, err := runGoSource(t, "s219generickey", src)
		qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
		qt.Assert(t, qt.Equals(stderr, ""))
		qt.Assert(t, qt.Equals(out, "1 2.5 9\n"))
	})

	t.Run("array copy and pointer slice field identity", func(t *testing.T) {
		const src = `package main
import "fmt"
type box struct { p *int; s []int }
func collect[T any](v T) []T { return []T{v} }
func main() {
	a := [2]int{1, 2}
	as := collect(a)
	a[0] = 8
	x := 3
	b := box{&x, []int{4}}
	bs := collect(b)
	*bs[0].p = 7
	bs[0].s[0] = 9
	fmt.Println(as[0], x, b.s[0])
}`
		out, stderr, err := runGoSource(t, "s219genericidentity", src)
		qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
		qt.Assert(t, qt.Equals(stderr, ""))
		qt.Assert(t, qt.Equals(out, "[1 2] 7 9\n"))
	})

	t.Run("wrong type remains rejected", func(t *testing.T) {
		const src = `package main
func bad[T any]() []T { return []T{"wrong"} }
func main() { _ = bad[int]() }
`
		_, err := gosource.Parse(strings.NewReader(src), "s219genericwrong.go", gosource.Options{RunMain: true})
		qt.Assert(t, qt.IsNotNil(err))
		qt.Assert(t, qt.StringContains(err.Error(), "cannot use"))
	})
}
