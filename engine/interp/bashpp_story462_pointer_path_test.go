//go:build full

// Sprint: #209; Story: #462; Story-ID: e348d2c13248
//
// Pointer field paths through pointer-typed fields. The r6 EPOINTER-TARGET
// corpus roots share one defect: a selection step through a pointer-typed
// field recorded no indirection, so the stored path later read the pointer
// value where struct storage was expected. It surfaced in two builders —
// bashPPAddress's selector walker (ken/embed.go's `&s.Subp.SubpSub`,
// orderedmap's `pn = &(*pn).left`) and bashPPBindLocalSelector's
// concatenated per-component field edges (ken/embed.go's automatic-&
// `s.Subp.SubpSub.test6()`) — and in a third, distinct shape in select5.go:
// a pointer-typed declaration whose value is a dependency's own pointer
// (`var recv = parse(...)` with parse returning *template.Template). These
// are outside-corpus reductions of those shapes plus the negative space: a
// nil pointer mid-chain must keep Go's nil-dereference run-time panic, and
// a non-pointer operand must keep the pointer-target refusal.
package interp_test

import (
	"strings"
	"testing"

	"github.com/go-quicktest/qt"

	"github.com/bashsharp/bashsharp/engine/interp"
)

// ken/embed.go shape: explicit & of a field reached through a pointer-typed
// embedded field, with a pointer-receiver method call on the result and
// writes through the same chain.
func TestStory462AddressOfFieldThroughPointerField(t *testing.T) {
	src := `package main
type Inner struct {
	b int
	a int
}
func (p *Inner) sum() int { return p.a + p.b }
type Mid struct {
	m int
	Inner
}
type S struct {
	x int
	*Mid
}
func main() {
	s := new(S)
	s.Mid = new(Mid)
	s.Mid.Inner.a = 2
	s.Mid.Inner.b = 3
	if (&s.Mid.Inner).sum() != 5 {
		panic("sum")
	}
	q := &s.Mid.Inner
	q.a = 7
	println(s.Mid.Inner.a, s.Mid.Inner.b)
}`
	out, stderr, err := runGoSource(t, "story462addr", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, "7 3\n"))
	qt.Assert(t, qt.Equals(out, ""))
}

// ken/embed.go automatic-& shape: the whole selector chain spelled on the
// call, so bashPPBindLocalSelector concatenates per-component selections;
// the pointer-typed component must keep its indirection in the merged path.
func TestStory462AutomaticAddressThroughPointerField(t *testing.T) {
	src := `package main
type Inner struct {
	b int
	a int
}
func (p *Inner) bump() int { p.a++; return p.a + p.b }
type S struct {
	x int
	*Inner
}
func main() {
	s := new(S)
	s.Inner = new(Inner)
	s.Inner.a = 1
	s.Inner.b = 10
	if s.Inner.bump() != 12 {
		panic("bump")
	}
	println(s.Inner.a)
}`
	out, stderr, err := runGoSource(t, "story462auto", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, "2\n"))
	qt.Assert(t, qt.Equals(out, ""))
}

// typeparam/orderedmap.go and orderedmapsimp.dir/a.go shape: a slot pointer
// re-aimed with `pn = &(*pn).left` — the address of a field selected through
// a dereferenced pointer-to-pointer — then written and read back.
func TestStory462SlotPointerTreeWalk(t *testing.T) {
	src := `package main
type node struct {
	key, val    int
	left, right *node
}
type Map struct{ root *node }
func (m *Map) find(key int) **node {
	pn := &m.root
	for *pn != nil {
		if key < (*pn).key {
			pn = &(*pn).left
		} else if key > (*pn).key {
			pn = &(*pn).right
		} else {
			return pn
		}
	}
	return pn
}
func main() {
	m := new(Map)
	for _, k := range []int{5, 3, 8, 4} {
		pn := m.find(k)
		if *pn != nil {
			panic("present")
		}
		*pn = &node{key: k, val: k * 10}
	}
	for _, k := range []int{3, 4, 5, 8} {
		pn := m.find(k)
		println((*pn).val)
	}
}`
	out, stderr, err := runGoSource(t, "story462tree", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, "30\n40\n50\n80\n"))
	qt.Assert(t, qt.Equals(out, ""))
}

// chan/select5.go shape: a declaration whose pointer-typed value is a
// dependency's own pointer, produced by an interpreter-declared function —
// the handle must be stored as a native value, not refused as a non-pointer.
func TestStory462NativePointerDeclaredFromCall(t *testing.T) {
	src := `package main
import (
	"os"
	"text/template"
)
func parse(name, s string) *template.Template {
	t, err := template.New(name).Parse(s)
	if err != nil {
		panic(err)
	}
	return t
}
var greet = parse("greet", "hello {{.}}\n")
func main() {
	if err := greet.Execute(os.Stdout, 42); err != nil {
		panic(err)
	}
}`
	out, stderr, err := runGoSource(t, "story462native", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(out, "hello 42\n"))
	qt.Assert(t, qt.Equals(stderr, ""))
}

// Negative: a nil pointer-typed field mid-chain is Go's nil dereference at
// run time — the recorded indirection must fault there, not report a
// pointer-target refusal.
func TestStory462NilPointerFieldChainPanics(t *testing.T) {
	src := `package main
type Inner struct{ a int }
type S struct {
	x int
	*Inner
}
func main() {
	s := new(S)
	s.Inner.a = 1
}`
	_, stderr, err := runGoSource(t, "story462nil", src)
	qt.Assert(t, qt.IsNotNil(err))
	qt.Assert(t, qt.IsTrue(strings.Contains(stderr, "nil pointer dereference")),
		qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.IsTrue(!strings.Contains(stderr, "EPOINTER-TARGET")),
		qt.Commentf("stderr: %s", stderr))
}

// Classic Bash++ shares bashPPAddress, so the recorded indirection must work
// there too: address of a field behind a pointer field writes through to the
// pointee, and the same chain over a nil pointer keeps the classic
// nil-dereference refusal rather than a pointer-target one.
func TestStory462ClassicPointerFieldChain(t *testing.T) {
	src := `type Inner struct { A int }
type S struct { N int; *Inner }
func main() {
 i := Inner{A: 1}
 s := S{N: 0, Inner: &i}
 fp := &s.Inner.A
 *fp = 5
 printf '%s\n' i.A
}
main()
`
	out, stderr, err := runBashSharpCall(t, src)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "5\n"))
}

func TestStory462ClassicNilPointerFieldChainRefused(t *testing.T) {
	src := `type Inner struct { A int }
type S struct { N int; *Inner }
func main() {
 var s S
 fp := &s.Inner.A
 *fp = 5
}
main()
`
	_, stderr, err := runBashSharpCall(t, src)
	qt.Assert(t, qt.ErrorIs(err, interp.ExitStatus(2)))
	qt.Assert(t, qt.IsTrue(strings.HasPrefix(stderr, "BASHPP-ENIL-DEREF:")),
		qt.Commentf("stderr: %s", stderr))
}
