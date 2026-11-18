//go:build full

package interp_test

import (
	"testing"

	"github.com/go-quicktest/qt"
)

// A scalar bound by `:=` from an untyped constant carries the constant's
// default type as its dynamic type when it is stored in an interface, both
// on declaration and as an interface-keyed map index. This is the eface
// portion of maplinear.go.
func TestS243PointerShortScalarDynamicType(t *testing.T) {
	src := `package main
func main() {
	x := 0
	var i interface{} = x
	m := map[interface{}]int{}
	for j := 0; j < 3; j++ {
		m[j] = 1
	}
	f := 1.5
	var g any = f
	_, isFloat := g.(float64)
	println(i.(int), len(m), isFloat)
}`
	out, stderr, err := runGoSource(t, "s243-pointer-short-scalar-dynamic", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(out, ""))
	qt.Assert(t, qt.Equals(stderr, "0 3 true\n"))
}

// A declared function named as a value stores in an interface with its
// declared signature as the dynamic type; a result name in the declaration
// does not change that signature (bug269.go).
func TestS243PointerDeclaredFuncInterfaceValue(t *testing.T) {
	src := `package main
func f() (ok bool) { return false }
func main() {
	var i interface{}
	i = f
	_ = i.(func() bool)
	g := i.(func() (bool))
	h := f
	var j any = h
	_, isFunc := j.(func() bool)
	_, isOther := j.(func() int)
	println(g(), isFunc, isOther)
}`
	out, stderr, err := runGoSource(t, "s243-pointer-declared-func-iface", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(out, ""))
	qt.Assert(t, qt.Equals(stderr, "false true false\n"))
}

// `type P = *T` is the very type *T: a P receiver selects *T's pointer
// methods (issue23489.go), including through a nil P.
func TestS243PointerAliasMethodSet(t *testing.T) {
	src := `package main
type T struct{ n int }
func (t *T) Foo() int { if t == nil { return -1 }; return t.n }
func (t T) Bar() int { return t.n + 10 }
type P = *T
type Q = P
func main() {
	var p P
	println(p.Foo())
	p = &T{n: 3}
	var q Q = p
	println(p.Foo(), q.Foo(), q.Bar())
}`
	out, stderr, err := runGoSource(t, "s243-pointer-alias-method-set", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(out, ""))
	qt.Assert(t, qt.Equals(stderr, "-1\n3 3 13\n"))
}

// A conversion to a defined pointer type — or to a type parameter bound to
// one — is a value of that type, so it assigns to a variable declared with
// the same name while still aliasing the operand's storage (issue49295.go).
func TestS243PointerDefinedPointerConversionAssign(t *testing.T) {
	src := `package main
type Token *[4]byte
func Read[T interface{ ~*[4]byte }](buf []byte) (t T, err error) {
	if n := len(t); len(buf) >= n {
		t = T(buf[:n])
		return
	}
	return
}
func direct(buf []byte) (t Token) {
	t = Token(buf[:4])
	return
}
func main() {
	b := []byte("abcdef")
	tok, err := Read[Token](b)
	tok[1] = 'X'
	var u Token
	u = direct(b)
	u[2] = 'Y'
	var v Token
	v = Token(u)
	println(string(b), err == nil, v[1] == 'X')
}`
	out, stderr, err := runGoSource(t, "s243-pointer-defined-conversion-assign", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(out, ""))
	qt.Assert(t, qt.Equals(stderr, "aXYdef true true\n"))
}

// A dependency-owned aggregate reached through a call result — `caller().frame`
// where frame is a runtime.Frame — keeps its fields in the worker, so the
// trailing selector is the same member read the named-local spelling already
// performs, and a method called on the call result sees the same value
// (issue21879.go).
func TestS243PointerCallRootedNativeField(t *testing.T) {
	src := `package main
import "runtime"
type call struct {
	frame runtime.Frame
	n     int
}
func caller() call {
	var pcs [3]uintptr
	n := runtime.Callers(1, pcs[:])
	frames := runtime.CallersFrames(pcs[:n])
	frame, _ := frames.Next()
	frame, _ = frames.Next()
	return call{frame: frame, n: 4}
}
func (c call) name() string { return c.frame.Function }
func main() {
	println(caller().frame.Function)
	println(caller().name())
	println(caller().frame.Line > 0, caller().n)
}`
	out, stderr, err := runGoSource(t, "s243-pointer-call-rooted-native-field", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(out, ""))
	qt.Assert(t, qt.Equals(stderr, "main.main\nmain.main\ntrue 4\n"))
}

// `return x.(T)` whose assertion fails raises the Go panic and unwinds; the
// interrupt reaching the return statement is not a diagnostic, so a deferred
// recover in the caller catches the panic and the program exits 0
// (typeparam/dottype.go). An unrecovered failure is still the panic.
func TestS243PointerReturnAssertionPanicRecovers(t *testing.T) {
	src := `package main
func f[T any](x interface{}) T {
	return x.(T)
}
type I interface{ foo() }
type myint int
func (myint) foo() {}
type myfloat float64
func (myfloat) foo() {}
func g[T I](x I) T {
	return x.(T)
}
func plain(x interface{}) int {
	return x.(int)
}
func shouldpanic(name string, x func()) {
	defer func() {
		e := recover()
		if e == nil {
			panic("didn't panic")
		}
		println("recovered", name)
	}()
	x()
}
func main() {
	var x interface{} = float64(3)
	var y I = myfloat(3)
	println(f[int](int(3)), plain(7), int(g[myint](myint(5))))
	shouldpanic("generic", func() { f[int](x) })
	shouldpanic("constrained", func() { g[myint](y) })
	shouldpanic("plain", func() { plain(x) })
	shouldpanic("plain-assign", func() { _ = plain(x) })
	println("done")
}`
	out, stderr, err := runGoSource(t, "s243-pointer-return-assert-recover", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(out, ""))
	qt.Assert(t, qt.Equals(stderr, "3 7 5\nrecovered generic\nrecovered constrained\nrecovered plain\nrecovered plain-assign\ndone\n"))

	unrecovered := `package main
func plain(x interface{}) int {
	return x.(int)
}
func main() {
	var x interface{} = float64(3)
	println("before")
	println(plain(x))
	println("after")
}`
	_, stderr, err = runGoSource(t, "s243-pointer-return-assert-unrecovered", unrecovered)
	qt.Assert(t, qt.IsNotNil(err))
	qt.Assert(t, qt.StringContains(stderr, "before\npanic: interface conversion: interface {} is float64, not int"))
	qt.Assert(t, qt.Not(qt.StringContains(stderr, "after")))
	qt.Assert(t, qt.Not(qt.StringContains(stderr, "scalar call interrupted")))
}

// A dependency result returned from a function declared to yield an interface
// crosses as the value it is, type name included, so the declared result boxes
// it with that dynamic type: a nil *map[int]bool minted by reflect asserts as
// *map[int]bool and not as anything else, and once asserted out it is the nil
// pointer — equal to nil, storable in a *map[int]bool variable — while the
// interface that held it is not nil (bug510.go).
func TestS243PointerNativeReturnBoxesInterface(t *testing.T) {
	src := `package main
import "reflect"
type A = map[int]bool
func F() interface{} {
	return reflect.New(reflect.TypeOf((*A)(nil))).Elem().Interface()
}
func H() interface{} {
	return reflect.ValueOf(3).Interface()
}
func main() {
	_, ok := H().(int)
	println("H int", ok)
	_, ok = F().(*map[int]bool)
	println("F ptr", ok)
	_, ok = F().(*map[int]string)
	println("F other", ok)
	_, ok = F().(map[int]bool)
	println("F elem", ok)
	v := F()
	p, ok := v.(*map[int]bool)
	println("v ptr", ok, p == nil, p != nil, v == nil)
	var r *map[int]bool = F().(*map[int]bool)
	var q *map[int]bool
	println("r nil", r == nil, q == r)
	r = &map[int]bool{1: true}
	println("r set", r == nil, (*r)[1])
}`
	out, stderr, err := runGoSource(t, "s243-pointer-native-return-boxes", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(out, ""))
	qt.Assert(t, qt.Equals(stderr, "H int true\nF ptr true\nF other false\nF elem false\nv ptr true true false false\nr nil true true\nr set false true\n"))

	dep := `package a
import "reflect"
type A = map[int]bool
func F() interface{} {
	return reflect.New(reflect.TypeOf((*A)(nil))).Elem().Interface()
}`
	mainSrc := `package main
import "test/a"
func main() {
	_, ok := a.F().(*map[int]bool)
	if !ok {
		panic("bad type")
	}
	_, other := a.F().(*map[string]bool)
	println("ok", ok, other)
}`
	out, stderr = runGoSourceMultiPackage(t, "s243-pointer-native-return-xpkg", mainSrc, "test/a", "a.go", dep)
	qt.Assert(t, qt.Equals(out, ""))
	qt.Assert(t, qt.Equals(stderr, "ok true false\n"))
}
