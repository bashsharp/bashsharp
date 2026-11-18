//go:build full

// Sprint: #209; Story: #463; Story-ID: a6f104b906d9
//
// The native-bridge type-identity family. A declared or named type must keep
// its authenticated identity as it crosses into an interface, back out through
// an assertion, and around the reflect bridge — and a mismatch must still
// panic with Go's own text, never be papered over. These are outside-corpus
// reductions of the six roots:
//
//   - named.go               a named scalar/slice/map/array/chan/string round-
//     trips to its own type through interface{}.
//   - reflectmethod1.go      reflect.TypeOf(v).Method(0).Func.Interface() is a
//   - reflectmethod3.go      func(M) value whose call re-enters the method.
//   - reflectmethod2.go      MethodByName reaches the same func(M) value.
//   - typeparam/issue47925b  a nested interface-to-interface conversion
//     E[T](I[T](x)) keeps the operand's *S, not the
//     source interface type nor a dereferenced S.
//   - fixedbugs/issue18911   identical anonymous structs from two packages are
//     two types; the panic says "different packages".
//
// The negative space is the point: an unexported field, a pointer, a named
// type and a bridge func type each keep their identity in the failure text.
package interp_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/go-quicktest/qt"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// named.go: every named type, boxed into interface{} with no conversion hint,
// asserts back to itself. asX confirms the value is usable as the named type.
func TestStory463NamedTypeIdentityRoundTrips(t *testing.T) {
	src := `package main
type Array [10]byte
type Bool bool
type Chan chan int
type Float float32
type Int int
type Map map[int]byte
type Slice []byte
type String string
func asArray(Array)   {}
func asBool(Bool)     {}
func asChan(Chan)     {}
func asFloat(Float)   {}
func asInt(Int)       {}
func asMap(Map)       {}
func asSlice(Slice)   {}
func asString(String) {}
func isArray(x interface{})  { _ = x.(Array) }
func isBool(x interface{})   { _ = x.(Bool) }
func isChan(x interface{})   { _ = x.(Chan) }
func isFloat(x interface{})  { _ = x.(Float) }
func isInt(x interface{})    { _ = x.(Int) }
func isMap(x interface{})    { _ = x.(Map) }
func isSlice(x interface{})  { _ = x.(Slice) }
func isString(x interface{}) { _ = x.(String) }
func main() {
	var a Array
	asArray(a); isArray(a); isArray(Array{})
	var b Bool = true
	asBool(b); isBool(b); isBool(!b); isBool(Bool(true))
	var c Chan = make(Chan)
	asChan(c); isChan(c); isChan(make(Chan))
	var f Float = 1
	asFloat(f); isFloat(f); isFloat(-f); isFloat(f + 1)
	var i Int = 1
	asInt(i); isInt(i); isInt(-i); isInt(i + 1)
	var m Map = make(Map)
	asMap(m); isMap(m); isMap(make(Map))
	var s Slice = make(Slice, 10)
	asSlice(s); isSlice(s)
	var str String = "hi"
	asString(str); isString(str); isString(str + "a"); isString(String("x"))
	println("ok")
}`
	out, stderr, err := runGoSource(t, "story463named", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(out, ""))
	qt.Assert(t, qt.Equals(stderr, "ok\n"))
}

// named.go, the address/deref corner: `*&v` must recover v's declared named
// type, not fall back to its predeclared base, or the assertion reads the
// wrong dynamic type.
func TestStory463AddressDerefPreservesNamedType(t *testing.T) {
	src := `package main
type Bool bool
type Chan chan int
type Map map[int]byte
type Slice []byte
func asBool(Bool){}
func isBool(x interface{}){ _ = x.(Bool) }
func isChan(x interface{}){ _ = x.(Chan) }
func isMap(x interface{}){ _ = x.(Map) }
func isSlice(x interface{}){ _ = x.(Slice) }
func main() {
	var b Bool = true
	asBool(*&b); isBool(*&b)
	var c Chan = make(Chan)
	isChan(*&c)
	var m Map = make(Map)
	isMap(*&m)
	var s Slice = make(Slice, 3)
	isSlice(*&s)
	println("ok")
}`
	_, stderr, err := runGoSource(t, "story463deref", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, "ok\n"))
}

// reflectmethod1.go / reflectmethod3.go: the method value materialized through
// reflect.Type.Method carries the bridge type func(M); calling it re-enters
// the interpreter's own method body.
func TestStory463ReflectMethodFuncValue(t *testing.T) {
	src := `package main
import "reflect"
var called = false
type M int
func (m M) UniqueMethodName() { called = true }
var v M
func main() {
	reflect.TypeOf(v).Method(0).Func.Interface().(func(M))(v)
	if !called { panic("UniqueMethodName not called") }
	println("ok")
}`
	_, stderr, err := runGoSource(t, "story463reflectmethod", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, "ok\n"))
}

// reflectmethod2.go: the same func(M) value reached through MethodByName on a
// reflect.Type held in a user interface.
func TestStory463ReflectMethodByNameFuncValue(t *testing.T) {
	src := `package main
import reflect1 "reflect"
var called = false
type M int
func (m M) UniqueMethodName() { called = true }
var v M
type MyType interface {
	MethodByName(string) (reflect1.Method, bool)
}
func main() {
	var t MyType = reflect1.TypeOf(v)
	m, _ := t.MethodByName("UniqueMethodName")
	m.Func.Interface().(func(M))(v)
	if !called { panic("UniqueMethodName not called") }
	println("ok")
}`
	_, stderr, err := runGoSource(t, "story463methodbyname", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, "ok\n"))
}

// typeparam/issue47925b.go: a nested nonempty-to-empty interface conversion of
// a *S must keep the *S dynamic type, so i.(*S) succeeds.
func TestStory463GenericNestedInterfaceConversion(t *testing.T) {
	src := `package main
type I[T any] interface { foo() }
type E[T any] interface {}
func f[T I[T]](x T) E[T] { return E[T](I[T](x)) }
type S struct { x int }
func (s *S) foo() {}
func main() {
	i := f(&S{x: 7})
	if i.(*S).x != 7 { panic("bad") }
	println("ok")
}`
	_, stderr, err := runGoSource(t, "story463generic", src)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("stderr: %s", stderr))
	qt.Assert(t, qt.Equals(stderr, "ok\n"))
}

// Negative: the same nested conversion holds *S; asserting the value type S
// must panic naming the true dynamic type *main.S, never a bare main.S.
func TestStory463NestedConversionKeepsPointerIdentity(t *testing.T) {
	src := `package main
type I[T any] interface { foo() }
type E[T any] interface {}
func f[T I[T]](x T) E[T] { return E[T](I[T](x)) }
type S struct { x int }
func (s *S) foo() {}
func main() {
	i := f(&S{x: 7})
	_ = i.(S)
	println("unreached")
}`
	out, stderr, err := runGoSource(t, "story463ptrid", src)
	qt.Assert(t, qt.IsNotNil(err))
	qt.Assert(t, qt.Equals(out, ""))
	qt.Assert(t, qt.StringContains(stderr, "interface conversion: main.E is *main.S, not main.S"))
}

// Negative: a named type asserted to a different named type of the same
// underlying kind panics naming both named types.
func TestStory463WrongNamedTypeAssertionPanics(t *testing.T) {
	src := `package main
type Int int
type Other int
func main() {
	var i interface{} = Int(5)
	_ = i.(Other)
	println("unreached")
}`
	_, stderr, err := runGoSource(t, "story463wrongnamed", src)
	qt.Assert(t, qt.IsNotNil(err))
	qt.Assert(t, qt.StringContains(stderr, "interface conversion: interface {} is main.Int, not main.Other"))
}

// Negative: the reflect-bridge method value keeps its func(M) identity, so an
// assertion to a different func type panics naming func(main.M).
func TestStory463ReflectMethodWrongFuncTypePanics(t *testing.T) {
	src := `package main
import "reflect"
type M int
func (m M) UniqueMethodName() {}
var v M
func main() {
	_ = reflect.TypeOf(v).Method(0).Func.Interface().(func(int))
	println("unreached")
}`
	_, stderr, err := runGoSource(t, "story463wrongfunc", src)
	qt.Assert(t, qt.IsNotNil(err))
	qt.Assert(t, qt.StringContains(stderr, "interface conversion: interface {} is func(main.M), not func(int)"))
}

// fixedbugs/issue18911.go: an anonymous struct with an unexported field is
// qualified by its package, so a.X's struct{ x int } and main's are two types.
// The panic must say "different packages", the wording package a's recover
// checks for.
func TestStory463CrossPackageStructIdentity(t *testing.T) {
	pkgA := "package a\n\nvar X interface{} = struct{ x int }{}\n"
	mainC := `package main
import (
	"./a"
	"strings"
)
func main() {
	defer func() {
		p, ok := recover().(error)
		if ok && strings.Contains(p.Error(), "different packages") {
			println("ok")
			return
		}
		panic(p)
	}()
	_ = a.X.(struct{ x int })
}`
	out, stderr := runGoSourceMultiPackage(t, "story463xpkg", mainC, "test/a", "a.go", pkgA)
	qt.Assert(t, qt.Equals(out, ""))
	qt.Assert(t, qt.Equals(stderr, "ok\n"))
}

// Guard the package/scope split the other direction: two identical anonymous
// structs declared in one package but different function scopes are still two
// types, and the panic says "different scopes", not "different packages".
func TestStory463SamePackageDifferentScopes(t *testing.T) {
	src := `package main
func a() interface{} { type T struct{ x int }; return T{7} }
func b() { type T struct{ x int }; _ = a().(T) }
func main() { b(); println("unreached") }`
	_, stderr, err := runGoSource(t, "story463scopes", src)
	qt.Assert(t, qt.IsNotNil(err))
	qt.Assert(t, qt.StringContains(stderr, "(types from different scopes)"))
}

// Linked packages are flattened under hygiene names, but runtime errors must
// keep the original package/type spelling. This is the assertion shape used
// by test/interface/embed3.go: the method name exists but its signature does
// not, so the runtime names the missing method rather than its linker name.
func TestStory674LinkedPackageRuntimeTypeText(t *testing.T) {
	mainSrc := `package main
import (
	"fmt"
	"./p"
)
func main() {
	defer func() { fmt.Println(recover()) }()
	var v interface{} = p.T{}
	_ = v.(p.I)
}`
	depSrc := `package p
type T struct{}
func (T) Foo() {}
type I interface { Foo(int) }`
	out, stderr := runGoSourceMultiPackage(t, "story674typetext", mainSrc, "test/p", "p.go", depSrc)
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "interface conversion: p.T is not p.I: missing method Foo\n"))
}

// runGoSourceMultiPackage loads a main source that relatively imports one
// helper package and runs main, returning stdout and stderr.
func runGoSourceMultiPackage(t *testing.T, name, mainSrc, importPath, depName, depSrc string) (string, string) {
	t.Helper()
	return runGoSourcePackageSet(t, name, mainSrc, []gosource.PackageSpec{{Path: importPath, Sources: []gosource.Source{{Name: depName, Data: []byte(depSrc)}}}})
}

func runGoSourcePackageSet(t *testing.T, name, mainSrc string, packages []gosource.PackageSpec) (string, string) {
	t.Helper()
	prog, err := gosource.Load([]gosource.Source{{Name: name + ".go", Data: []byte(mainSrc)}}, gosource.Options{
		RunMain:    true,
		ImportBase: "test",
		Packages:   packages,
	})
	if err != nil {
		t.Fatalf("gosource.Load: %v", err)
	}
	var out, errout bytes.Buffer
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(t.TempDir()), interp.StdIO(nil, &out, &errout))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := runner.Run(ctx, prog.File); err != nil {
		t.Fatalf("Runner: %v; stdout=%q stderr=%q", err, out.String(), errout.String())
	}
	return out.String(), errout.String()
}

// A satisfied first method must never be blamed for a later absent method
// or a later method whose signature differs. Check both declaration orders.
func TestS243TypeAssertionReportsActualFailingMethod(t *testing.T) {
	for _, methods := range []string{"A(); Z(int)", "Z(int); A()"} {
		for _, z := range []string{"", "func (T) Z() {}"} {
			t.Run(methods+z, func(t *testing.T) {
				mainSrc := `package main
import ("fmt"; "./p")
func main() {
 defer func() { fmt.Println(recover()) }()
 var v any = p.T{}
 _ = v.(p.I)
}`
				depSrc := "package p\ntype T struct{}\nfunc (T) A() {}\n" + z + "\ntype I interface { " + methods + " }\n"
				out, stderr := runGoSourceMultiPackage(t, "s243missingmethod", mainSrc, "test/p", "p.go", depSrc)
				qt.Assert(t, qt.Equals(stderr, ""))
				qt.Assert(t, qt.Equals(out, "interface conversion: p.T is not p.I: missing method Z\n"))
			})
		}
	}
}

func TestS243TypeAssertionAcceptsAllMethods(t *testing.T) {
	const mainSrc = `package main
import ("fmt"; "./p")
func main() { var v any = p.T{}; x := v.(p.I); x.A(); x.Z(1); fmt.Println("ok") }
`
	const depSrc = `package p
type T struct{}
func (T) A() {}
func (T) Z(int) {}
type I interface { Z(int); A() }
`
	out, stderr := runGoSourceMultiPackage(t, "s243allmethods", mainSrc, "test/p", "p.go", depSrc)
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, "ok\n"))
}

// Known empty method sets still raise Go's recoverable assertion panic. The
// required methods are deliberately out of lexical order to verify that the
// first missing method is selected from the actual (empty) method set.
func TestS243TypeAssertionKnownEmptyMethodSets(t *testing.T) {
	for _, expr := range []string{"[]int{}", "map[int]int{}", "[1]int{}", "func() {}", "make(chan int)", "struct{}{}", "1", "true"} {
		t.Run(expr, func(t *testing.T) {
			differGoSource(t, `package main
import "fmt"
type I interface { Z(); A() }
func main() {
 defer func() { fmt.Println(recover()) }()
 var x any = `+expr+`
 _ = x.(I)
}
`, nil, "")
		})
	}
}
