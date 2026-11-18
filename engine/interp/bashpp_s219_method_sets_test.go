//go:build full

// Sprint: #219; Story: #462; Story-ID: e348d2c13248
//
// Interface method sets and typed selectors. Outside-corpus reductions of
// the roots this lane owns, each paired with the negative that keeps the
// mechanism honest:
//
//   - alias3            an interface-to-interface assignment compares method
//     signatures through declared aliases, across packages.
//   - bug424            an unexported method promoted from two packages at
//     the same depth is selected by the selecting package, not ambiguous.
//   - bug474            a method value `t.M` with a pointer receiver crosses
//     to a dependency's callback (sync.Once.Do) from an addressable var.
//   - issue59709        a method value is a struct literal's func field.
//   - method.go         an anonymous struct type implements an interface
//   - issue4590         through its embedded fields' promoted methods.
//   - issue24547        the shallowest promoted method wins even when a
//     deeper one is a program method and the shallow one is native.
//   - ken/rob1          a local bound to a pointer field keeps a typed
//     selector path for `i.item.Print()`.
//   - struct0           a zero-size struct asserted out of an interface.
package interp_test

import (
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/go-quicktest/qt"
)

// runS219 runs a single-file program and returns stdout, stderr and the
// runner error (nil on a clean exit).
func runS219(t *testing.T, src string) (string, string, error) {
	t.Helper()
	return runGoSource(t, "s219", src)
}

func TestS219InterfaceAliasSignatures(t *testing.T) {
	src := `package main
type (
	Float64 = float64
	Int int
	IntAlias = Int
	IntAlias2 = IntAlias
)
type I1 interface {
	M1(IntAlias2) Float64
}
type I2 = interface {
	M1(Int) float64
}
type S struct{}
func (S) M1(x IntAlias) float64 { return Float64(x) * 2 }
func main() {
	var i1 I1 = S{}
	var i2 I2 = i1
	println(i2.M1(21) == 42)
	var _ I1 = i2
}
`
	_, stderr, err := runS219(t, src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr=%q", stderr))
	qt.Assert(t, qt.Equals(stderr, "true\n"))
}

// The negative: an alias makes signatures equal only when it names the same
// type. A distinct defined type with the same underlying type does not.
func TestS219InterfaceAliasSignaturesNegative(t *testing.T) {
	src := `package main
type Int int
type Other int
type I1 interface{ M1(Int) }
type S struct{}
func (S) M1(Other) {}
func main() {
	var x any = S{}
	_, ok := x.(I1)
	println(ok)
}
`
	_, stderr, err := runS219(t, src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr=%q", stderr))
	qt.Assert(t, qt.StringContains(stderr, "false"))
}

func TestS219UnexportedPromotionByPackage(t *testing.T) {
	lib := `package lib
type I interface{ m() string }
type T struct{}
func (t *T) m() string { return "lib.T.m" }
func Call(i I) string { return i.m() }
`
	mainSrc := `package main
import "./lib"
type localI interface{ m() string }
type localT struct{}
func (t *localT) m() string { return "main.localT.m" }
type myT2 struct {
	localT
	lib.T
}
type myT3 struct {
	lib.T
	localT
}
func main() {
	var i localI
	i = new(myT2)
	println(i.m())
	t3 := new(myT3)
	println(t3.m())
	i = new(myT3)
	println(i.m())
	var t4 struct {
		localT
		lib.T
	}
	println(t4.m())
	i = &t4
	println(i.m())
	println(lib.Call(new(myT2)))
}
`
	_, stderr := runGoSourceMultiPackage(t, "s219bug424", mainSrc, "test/lib", "lib.go", lib)
	want := "main.localT.m\nmain.localT.m\nmain.localT.m\nmain.localT.m\nmain.localT.m\nlib.T.m\n"
	qt.Assert(t, qt.Equals(stderr, want))
}

// The negative: two promoted methods of the SAME package at the same depth
// stay ambiguous, and an assertion to an interface naming one reports it.
func TestS219UnexportedPromotionAmbiguousNegative(t *testing.T) {
	src := `package main
type I interface{ m() string }
type A struct{}
func (A) m() string { return "A" }
type B struct{}
func (B) m() string { return "B" }
type AB struct {
	A
	B
}
func main() {
	var x any = AB{}
	_, ok := x.(I)
	println(ok)
}
`
	_, stderr, err := runS219(t, src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr=%q", stderr))
	qt.Assert(t, qt.StringContains(stderr, "false"))
}

func TestS219MethodValueToNativeCallback(t *testing.T) {
	src := `package main
import "sync"
var called = 0
type T struct {
	once sync.Once
	n    int
}
func (t *T) M() { called++; t.n = 7 }
func main() {
	var t T
	t.once.Do(t.M)
	t.once.Do(t.M)
	println(called, t.n)
}
`
	_, stderr, err := runS219(t, src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr=%q", stderr))
	qt.Assert(t, qt.StringContains(stderr, "1 7"))
}

func TestS219MethodValueStructField(t *testing.T) {
	src := `package main
type Res struct{ x int }
func (r *Res) teardown() { r.x = 9 }
func (r *Res) name() string { return "mem" }
type Config struct {
	Name     func() string
	TearDown func()
}
func main() {
	res := &Res{x: 1}
	cfg := Config{Name: res.name, TearDown: res.teardown}
	cfg.TearDown()
	println(cfg.Name(), res.x)
	var f func() = res.teardown
	res.x = 0
	f()
	println(res.x)
}
`
	_, stderr, err := runS219(t, src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr=%q", stderr))
	qt.Assert(t, qt.StringContains(stderr, "mem 9\n9"))
}

func TestS219AnonymousStructImplements(t *testing.T) {
	src := `package main
type Val interface{ val() int }
type S struct{ a int }
func (s S) val() int { return s.a }
type P struct{ b int }
func (p *P) val() int { return p.b }
func val(v Val) int { return v.val() }
func main() {
	var zs struct{ S }
	zs.a = 1
	var zps struct{ *P }
	zps.P = &P{2}
	var zi struct{ Val }
	zi.Val = S{3}
	println(val(zs), val(zps), val(zi))
	var v Val = zs
	println(v.val())
	var x any = zps
	_, ok := x.(Val)
	println(ok)
}
`
	_, stderr, err := runS219(t, src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr=%q", stderr))
	qt.Assert(t, qt.StringContains(stderr, "1 2 3\n1\ntrue"))
}

// The negative: an anonymous struct whose embedded field has only a
// pointer-receiver method does not implement the interface by value.
func TestS219AnonymousStructImplementsNegative(t *testing.T) {
	src := `package main
type Val interface{ val() int }
type P struct{ b int }
func (p *P) val() int { return p.b }
func main() {
	var x any = struct{ P }{P{1}}
	_, ok := x.(Val)
	println(ok)
	var y any = struct{ n int }{1}
	_, ok = y.(Val)
	println(ok)
}
`
	_, stderr, err := runS219(t, src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr=%q", stderr))
	qt.Assert(t, qt.StringContains(stderr, "false\nfalse"))
}

func TestS219ShallowestPromotedMethodWins(t *testing.T) {
	src := `package main
import (
	"bytes"
	"fmt"
)
type mystruct struct{ f int }
func (t mystruct) String() string { return "FAIL" }
func main() {
	type deep struct{ mystruct }
	s := struct {
		deep
		*bytes.Buffer
	}{deep{}, bytes.NewBufferString("ok")}
	println(s.String())
	var i fmt.Stringer = s
	println(i.String())
}
`
	_, stderr, err := runS219(t, src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr=%q", stderr))
	qt.Assert(t, qt.StringContains(stderr, "ok\nok"))
}

// The controls keep interface dispatch on the same selector rules as direct
// dispatch: a shallow original method beats a deeper native one, while two
// candidates at the winning depth leave the method ambiguous.
func TestS219ShallowestPromotedMethodWinsControls(t *testing.T) {
	src := `package main
import (
	"bytes"
	"fmt"
)
type local struct{}
func (local) String() string { return "local" }
type deepNative struct{ *bytes.Buffer }
type shallowOriginal struct {
	local
	deepNative
}
type ambiguous struct {
	local
	*bytes.Buffer
}
func main() {
	s := shallowOriginal{local{}, deepNative{bytes.NewBufferString("wrong")}}
	var i fmt.Stringer = s
	println(s.String())
	println(i.String())
}
`
	_, stderr, err := runS219(t, src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr=%q", stderr))
	qt.Assert(t, qt.StringContains(stderr, "local\nlocal"))

	ambiguousSrc := `package main
import (
	"bytes"
	"fmt"
)
type local struct{}
func (local) String() string { return "local" }
type ambiguous struct {
	local
	*bytes.Buffer
}
func main() {
	var x any = ambiguous{local{}, bytes.NewBufferString("wrong")}
	i, ok := x.(fmt.Stringer)
	println(ok)
	if ok {
		println(i.String())
	}
}
`
	_, stderr, err = runS219(t, ambiguousSrc)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr=%q", stderr))
	qt.Assert(t, qt.Equals(stderr, "false\n"))
}

func TestS219PointerFieldLocalSelectorPath(t *testing.T) {
	src := `package main
type Item interface{ Print() string }
type ListItem struct {
	item Item
	next *ListItem
}
type List struct{ head *ListItem }
func (list *List) Insert(i Item) {
	item := new(ListItem)
	item.item = i
	item.next = list.head
	list.head = item
}
func (list *List) Print() string {
	r := ""
	i := list.head
	for i != nil {
		r += i.item.Print()
		i = i.next
	}
	return r
}
type Integer struct{ val int }
func (this *Integer) Print() string { return string(rune(this.val + '0')) }
func main() {
	list := new(List)
	for i := 0; i < 4; i++ {
		list.Insert(&Integer{i})
	}
	println(list.Print())
}
`
	_, stderr, err := runS219(t, src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr=%q", stderr))
	qt.Assert(t, qt.StringContains(stderr, "3210"))
}

// The negative: the local keeps the pointer's identity as well as its type,
// so a nil head bound to `i` compares equal to nil and the selector call
// through it is Go's nil dereference, not a typed-path refusal.
func TestS219PointerFieldLocalSelectorPathNegative(t *testing.T) {
	src := `package main
type Item interface{ Print() string }
type ListItem struct {
	item Item
	next *ListItem
}
type List struct{ head *ListItem }
func main() {
	list := new(List)
	i := list.head
	println(i == nil)
	println(i.item.Print())
}
`
	_, stderr, err := runS219(t, src)
	qt.Assert(t, qt.IsNotNil(err))
	qt.Assert(t, qt.StringContains(stderr, "true\n"))
	qt.Assert(t, qt.StringContains(stderr, "invalid memory address or nil pointer dereference"))
	qt.Assert(t, qt.IsFalse(strings.Contains(stderr, "BASHPP-ESELECTOR")), qt.Commentf("stderr=%q", stderr))
}

func TestS219ZeroSizeStructAssertion(t *testing.T) {
	src := `package main
func recv(c chan interface{}) struct{} {
	return (<-c).(struct{})
}
var m = make(map[interface{}]int)
func recv1(c chan interface{}) {
	defer rec()
	m[(<-c).(struct{})] = 0
}
func rec() { recover() }
func main() {
	c := make(chan interface{})
	go recv(c)
	c <- struct{}{}
	go recv1(c)
	c <- struct{}{}
	var x interface{} = struct{}{}
	_ = x.(struct{})
	println(len(m) <= 1)
}
`
	_, stderr, err := runS219(t, src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr=%q", stderr))
	qt.Assert(t, qt.StringContains(stderr, "true"))
}

// The negative: asserting an interface holding a zero-size struct to a
// different struct type panics with Go's own text.
func TestS219ZeroSizeStructAssertionNegative(t *testing.T) {
	src := `package main
func main() {
	var x interface{} = struct{}{}
	_ = x.(struct{ n int })
}
`
	_, stderr, err := runS219(t, src)
	qt.Assert(t, qt.IsNotNil(err))
	qt.Assert(t, qt.IsTrue(strings.Contains(stderr, "interface conversion")), qt.Commentf("stderr=%q", stderr))
}

// The negatives for the receive-assert form: a failed `(<-c).(T)` panics
// with Go's own text, a guard's bare `recover()` swallows it without turning
// the guarded frame into a failure, and the unguarded one still terminates.
func TestS219ReceiveAssertionMismatch(t *testing.T) {
	src := `package main
func recv(c chan interface{}) {
	defer rec()
	_ = (<-c).(int)
	println("unreached")
}
func rec() { recover() }
func boom(c chan interface{}) {
	_ = (<-c).(int)
}
func main() {
	c := make(chan interface{}, 2)
	c <- "s"
	c <- "t"
	recv(c)
	println("guarded")
	boom(c)
	println("unreached")
}
`
	_, stderr, err := runS219(t, src)
	qt.Assert(t, qt.IsNotNil(err))
	qt.Assert(t, qt.StringContains(stderr, "guarded\npanic: interface conversion: interface {} is string, not int"))
	qt.Assert(t, qt.IsFalse(strings.Contains(stderr, "unreached")), qt.Commentf("stderr=%q", stderr))
}

var _ = gosource.Options{}
