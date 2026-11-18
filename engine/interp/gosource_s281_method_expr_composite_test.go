//go:build full

package interp_test

import (
	"testing"

	"github.com/go-quicktest/qt"
)

// Sprint: #281; Story: #810; Story-ID: 48c1146a3ab0
//
// A method expression — `T.M` or `(*T).M` — names a type's method as a
// function whose first parameter is the receiver. When it fills a func-typed
// composite element (`one: (*state).constInt64` in cmd/compile/internal/ssagen
// ssa.go), the value paths must bind the forwarding closure the declaration
// form already binds, not read `(*state)` as a value: `state` is a type, so
// dereferencing it asked bashPPPointerExprValue for a pointer the name is not
// and raised `BASHPP-EPOINTER-TARGET: <type> is not a pointer`. The type is
// spelled with its flattened runtime name (`__gosource_pkg_0_state`) once the
// declaring package is a mapped dependency, which is where ssagen tripped.
func TestGoSourceS281MethodExprCompositeElement(t *testing.T) {
	t.Run("pointer_receiver_struct_field", func(t *testing.T) {
		src := `package main
import "fmt"
type state struct{ base int64 }
func (s *state) one(x int64) int64 { return s.base + x }
type tab struct{ fn func(*state, int64) int64 }
var table = tab{fn: (*state).one}
func main() { s := &state{base: 10}; fmt.Println(table.fn(s, 5)) }`
		differGoSource(t, src, nil, "")
	})

	t.Run("value_receiver_struct_field", func(t *testing.T) {
		src := `package main
import "fmt"
type state struct{ base int64 }
func (s state) one(x int64) int64 { return s.base + x }
type tab struct{ fn func(state, int64) int64 }
var table = tab{fn: state.one}
func main() { fmt.Println(table.fn(state{base: 10}, 5)) }`
		differGoSource(t, src, nil, "")
	})

	t.Run("local_composite", func(t *testing.T) {
		src := `package main
import "fmt"
type state struct{ base int64 }
func (s *state) one(x int64) int64 { return s.base + x }
type tab struct{ fn func(*state, int64) int64 }
func main() {
	table := tab{fn: (*state).one}
	s := &state{base: 10}
	fmt.Println(table.fn(s, 5))
}`
		differGoSource(t, src, nil, "")
	})

	t.Run("slice_element", func(t *testing.T) {
		src := `package main
import "fmt"
type state struct{ base int64 }
func (s *state) one(x int64) int64 { return s.base + x }
var fns = []func(*state, int64) int64{(*state).one}
func main() { s := &state{base: 10}; fmt.Println(fns[0](s, 5)) }`
		differGoSource(t, src, nil, "")
	})

	t.Run("map_value", func(t *testing.T) {
		src := `package main
import "fmt"
type state struct{ base int64 }
func (s *state) one(x int64) int64 { return s.base + x }
var fns = map[string]func(*state, int64) int64{"a": (*state).one}
func main() { s := &state{base: 10}; fmt.Println(fns["a"](s, 5)) }`
		differGoSource(t, src, nil, "")
	})

	// A field selector whose operand is an ordinary value must still be read,
	// not mistaken for a method expression.
	t.Run("field_selector_still_read", func(t *testing.T) {
		src := `package main
import "fmt"
type inner struct{ v int }
type outer struct{ n int }
var i = inner{v: 7}
var o = outer{n: i.v}
func main() { fmt.Println(o.n) }`
		differGoSource(t, src, nil, "")
	})
}

// The declaring package flattens to a mapped dependency, so the receiver type
// is spelled with its `__gosource_pkg_0_` runtime name — the exact shape that
// ssagen's `one: (*state).constInt64` presents once ssagen is the mapped
// package under test.
func TestGoSourceS281MethodExprMappedPackage(t *testing.T) {
	depSrc := `package p
type state struct{ base int64 }
func (s *state) one(x int64) int64 { return s.base + x }
type tab struct{ fn func(*state, int64) int64 }
var table = tab{fn: (*state).one}
func Run() int64 {
	s := &state{base: 10}
	return table.fn(s, 5)
}`
	mainSrc := `package main
import ("fmt"; "./p")
func main() { fmt.Println(p.Run()) }`
	out, stderr := runGoSourceMultiPackage(t, "s281methodexpr", mainSrc, "test/p", "p.go", depSrc)
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "15\n"))
}
