//go:build full

package interp_test

// Sprint: #219; Story: #460; Story-ID: d8e7d58f362b

import (
	"strings"
	"testing"

	"github.com/go-quicktest/qt"

	"github.com/bashsharp/bashsharp/engine/gosource"
)

func TestS219MapUpdate(t *testing.T) {
	t.Run("operand and key exactly once", func(t *testing.T) {
		src := `package main
import "fmt"
var shared = map[int]int{7: 2}
var mapCalls, keyCalls int
func table() map[int]int { mapCalls++; return shared }
func key() int { keyCalls++; return 7 }
func main() {
	table()[key()]++
	fmt.Println(shared[7], mapCalls, keyCalls)
}`
		out, stderr, err := runGoSource(t, "s219_map_once", src)
		qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
		qt.Assert(t, qt.Equals(stderr, ""))
		qt.Assert(t, qt.Equals(out, "3 1 1\n"))
	})

	t.Run("nil map update panics", func(t *testing.T) {
		src := `package main
import "fmt"
var keyCalls, rhsCalls int
func key() string { keyCalls++; return "missing" }
func rhs() int { rhsCalls++; return 1 }
func main() {
	defer func() { fmt.Println(keyCalls, rhsCalls, recover()) }()
	var m map[string]int
	m[key()] += rhs()
}`
		out, stderr, err := runGoSource(t, "s219_nil_map_update", src)
		qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
		qt.Assert(t, qt.Equals(stderr, ""))
		qt.Assert(t, qt.Equals(out, "1 1 assignment to entry in nil map\n"))
	})

	t.Run("instantiated generic struct key", func(t *testing.T) {
		src := `package main
import "fmt"
type key2[T1, T2 comparable] struct { f1 T1; f2 T2 }
type metric[T1, T2 comparable] struct { m map[key2[T1, T2]]int }
func (m *metric[T1, T2]) add(v1 T1, v2 T2) {
	if m.m == nil { m.m = make(map[key2[T1, T2]]int) }
	m.m[key2[T1, T2]{v1, v2}]++
}
func main() {
	var m metric[int, float64]
	m.add(4, 2.5)
	m.add(4, 2.5)
	fmt.Println(m.m[key2[int, float64]{4, 2.5}])
}`
		out, stderr, err := runGoSource(t, "s219_generic_map_key", src)
		qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
		qt.Assert(t, qt.Equals(stderr, ""))
		qt.Assert(t, qt.Equals(out, "2\n"))
	})

	t.Run("map element remains non-addressable", func(t *testing.T) {
		src := `package main
func main() { m := map[int]int{1: 2}; _ = &m[1] }`
		_, err := gosource.Parse(strings.NewReader(src), "s219_map_address.go", gosource.Options{RunMain: true})
		qt.Assert(t, qt.IsNotNil(err))
		qt.Assert(t, qt.StringContains(err.Error(), "cannot take address"))
	})
}
