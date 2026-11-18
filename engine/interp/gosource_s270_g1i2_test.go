package interp_test

// Sprint: #270; Story: #756; Story-ID: a0503101d15a

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// An element of an interpreter-held collection of a dependency-defined basic
// type (go/token.Token) is the scalar it is, whether the array is the original
// or a copy (cmd/compile/internal/types2: var t [24]Token = toks).
func TestGoSourceS270G1i2ImportedScalarElement(t *testing.T) {
	out, stderr, err := runS270G1Source(t, "s270-imported-scalar-element", `package main
import (
	"fmt"
	"go/token"
)
var toks = [...]token.Token{token.ADD, token.SUB}
func main() {
	var t [2]token.Token = toks
	u := toks
	s := []token.Token{token.MUL}
	fmt.Println(toks[0], t[0], u[1], s[0], t[1] == token.SUB)
}`)
	if err != nil || out != "+ + - * true\n" {
		t.Fatalf("err=%v stdout=%q stderr=%q", err, out, stderr)
	}
}

// A value whose declared type is an alias of a defined type crosses with the
// defined type's identity, so fmt finds its String method
// (cmd/compile/internal/syntax: type token = Token).
func TestGoSourceS270G1i2AliasStringer(t *testing.T) {
	out, stderr, err := runS270G1Source(t, "s270-alias-stringer", `package main
import "fmt"
type Tkn uint
type tkn = Tkn
func (t Tkn) String() string { return fmt.Sprint("tok", uint(t)) }
func main() {
	var x tkn = 2
	fmt.Println(x)
	y := tkn(3)
	fmt.Println(y, x)
}`)
	if err != nil || out != "tok2\ntok3 tok2\n" {
		t.Fatalf("err=%v stdout=%q stderr=%q", err, out, stderr)
	}
}

// An indexed write through a dependency-owned map or slice stores the value
// at the element type the dependency declares
// (cmd/compile/internal/devirtualize: p.WeightedCG.IRNodes[name] = n).
func TestGoSourceS270G1i2NativeIndexedWrite(t *testing.T) {
	out, stderr, err := runS270G1Source(t, "s270-native-indexed-write", `package main
import (
	"fmt"
	"go/ast"
)
type H struct{ f *ast.File }
func (h *H) add(name string) *ast.Object {
	n := &ast.Object{Name: name}
	h.f.Scope.Objects[name] = n
	return n
}
func nilMap() (msg string) {
	defer func() { msg = fmt.Sprint(recover()) }()
	s := &ast.Scope{}
	s.Objects["x"] = nil
	return ""
}
func main() {
	h := &H{f: &ast.File{Scope: ast.NewScope(nil), Decls: make([]ast.Decl, 1)}}
	o := h.add("x")
	h.f.Decls[0] = &ast.BadDecl{}
	fmt.Println(h.f.Scope.Objects["x"] == o, h.f.Scope.Lookup("x").Name, h.f.Decls[0] != nil)
	fmt.Println(nilMap())
}`)
	if err != nil || out != "true x true\nassignment to entry in nil map\n" {
		t.Fatalf("err=%v stdout=%q stderr=%q", err, out, stderr)
	}
}

// A test function the dependency's testing.tRunner calls back into runs to
// completion. It first pinned the diagnostic `sync.OnceValue` produced
// ("retained original function callbacks") as the program's outcome;
// 89f34b5f admitted synchronous original method callbacks, so the body now
// passes and the retained t.Cleanup still runs after it.
func TestGoSourceS270G1i2TestBodyDiagnostic(t *testing.T) {
	xtest := gosource.PackageSpec{Path: "example.com/lib_test", Sources: []gosource.Source{s249Source("lib_test.go", `package lib_test

import (
	"fmt"
	"sync"
	"testing"
)

func TestCleanup(t *testing.T) {
	t.Cleanup(func() { fmt.Println("cleanup ran") })
	fmt.Println("body ran")
}

func TestOnce(t *testing.T) {
	var calls int
	once := sync.OnceValue(func() int { calls++; return 1 })
	if first, second := once(), once(); first != 1 || second != 1 || calls != 1 {
		t.Fatalf("OnceValue: values=(%d,%d) calls=%d; want (1,1) and one call", first, second, calls)
	}
	var pairCalls int
	pair := sync.OnceValues(func() (int, error) { pairCalls++; return 2, nil })
	first, firstErr := pair()
	second, secondErr := pair()
	if first != 2 || second != 2 || firstErr != nil || secondErr != nil || pairCalls != 1 {
		t.Fatalf("OnceValues: values=(%d,%d) errors=(%v,%v) calls=%d; want (2,2), nil errors and one call", first, second, firstErr, secondErr, pairCalls)
	}
	var plain sync.Once
	var plainCalls int
	plain.Do(func() { plainCalls++ })
	plain.Do(func() { plainCalls++ })
	if plainCalls != 1 {
		t.Fatalf("Once.Do calls=%d; want one call", plainCalls)
	}
}
`)}}
	driver := s249Source("_testmain.go", `package main

import (
	"os"
	"testing"
	"testing/internal/testdeps"

	_xtest "example.com/lib_test"
)

var tests = []testing.InternalTest{
	{"TestCleanup", _xtest.TestCleanup},
	{"TestOnce", _xtest.TestOnce},
}

func main() {
	m := testing.MainStart(testdeps.TestDeps{}, tests, nil, nil, nil)
	os.Exit(m.Run())
}
`)
	got, err := runS249PackageTestMain(t, []gosource.Source{driver}, []gosource.PackageSpec{xtest})
	if err != nil || !strings.Contains(got.stdout, "PASS") {
		t.Fatalf("err=%v outcome=%+v; want both test bodies to pass", err, got)
	}
	// testing.T.Cleanup retains its function until the test finishes.
	if !strings.Contains(got.stdout, "body ran\ncleanup ran\n") {
		t.Fatalf("stdout=%q; want the retained cleanup after the body", got.stdout)
	}
}

// reflect.TypeOf of a copied value whose type has pointer methods and
// reference-bearing fields inspects only the type
// (cmd/compile/internal/types TestSizeof).
func TestGoSourceS270G1i2TypeOfReferenceBearingValue(t *testing.T) {
	out, stderr, err := runS270G1Source(t, "s270-typeof-reference-bearing", `package main
import (
	"fmt"
	"reflect"
)
type Func struct {
	params []int
	m      map[string]int
}
func (f *Func) Add(n int) { f.params = append(f.params, n) }
func main() {
	var tests = []struct{ val any }{{Func{}}, {Func{params: []int{1}}}}
	for _, tt := range tests {
		fmt.Println(reflect.TypeOf(tt.val).Size(), reflect.TypeOf(tt.val).Kind())
	}
}`)
	if err != nil || out != "32 struct\n32 struct\n" {
		t.Fatalf("err=%v stdout=%q stderr=%q", err, out, stderr)
	}
}

// A field selected through a call that returns a pointer is addressable
// (cmd/compile/internal/types: s.StructType().ParamTuple = true).
func TestGoSourceS270G1i2CallResultPointerFieldAssign(t *testing.T) {
	out, stderr, err := runS270G1Source(t, "s270-call-result-pointer-field", `package main
import "fmt"
type Struct struct{ ParamTuple bool; n int }
type Type struct{ extra any }
func (t *Type) StructType() *Struct { return t.extra.(*Struct) }
func mk() *Type { return &Type{extra: &Struct{}} }
func get(s *Struct) *Struct { return s }
func main() {
	s := mk()
	s.StructType().ParamTuple = true
	s.StructType().n += 2
	get(s.StructType()).n++
	fmt.Println(*s.extra.(*Struct))
}`)
	if err != nil || out != "{true 3}\n" {
		t.Fatalf("err=%v stdout=%q stderr=%q", err, out, stderr)
	}
}

// slices.SortStableFunc over a slice of pointers hands the comparison the
// pointers the slice holds (cmd/compile/internal/types expandiface:
// slices.SortStableFunc(methods, func(a, b *Field) int {...})).
func TestGoSourceS270G1i2SortFuncPointerElements(t *testing.T) {
	out, stderr, err := runS270G1Source(t, "s270-sortfunc-pointer-elements", `package main
import (
	"cmp"
	"fmt"
	"slices"
)
type Field struct{ name string; seen int }
func main() {
	a, b, c := &Field{name: "b"}, &Field{name: "a"}, &Field{name: "b"}
	fields := []*Field{a, b, c}
	slices.SortStableFunc(fields, func(x, y *Field) int {
		x.seen++
		return cmp.Compare(x.name, y.name)
	})
	fmt.Println(fields[0] == b, fields[1] == a, fields[2] == c, a.seen+b.seen+c.seen > 0)
	slices.SortFunc(fields, func(x, y *Field) int { return cmp.Compare(y.name, x.name) })
	fmt.Println(fields[2].name)
}`)
	if err != nil || out != "true true true true\na\n" {
		t.Fatalf("err=%v stdout=%q stderr=%q", err, out, stderr)
	}
}

func runS270G1i2Linked(t *testing.T, identity string, mainSource string, pkg gosource.PackageSpec) (string, string, error) {
	t.Helper()
	program, err := gosource.Load([]gosource.Source{{Name: "main.go", Data: []byte(mainSource)}}, gosource.Options{
		RunMain: true, ImportPath: identity, Packages: []gosource.PackageSpec{pkg},
	})
	if err != nil {
		t.Fatalf("gosource.Load: %v", err)
	}
	var out, errout bytes.Buffer
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(t.TempDir()), interp.StdIO(nil, &out, &errout), interp.GoSourceIdentity(identity, false))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	err = runner.Run(ctx, program.File)
	return out.String(), errout.String(), err
}

// A linked package's imports resolve under that package's own identity, as
// the front end checked them and as cmd/go applies the internal rule per
// importing package: cmd/compile's own files import its architecture
// packages while the program identity is cmd/compile.test.
func TestGoSourceS270G1i2LinkedPackageImportIdentity(t *testing.T) {
	pkg := gosource.PackageSpec{Path: "cmd/s270g1i2", Sources: []gosource.Source{s249Source("p.go", `package s270g1i2

import "internal/goversion"

func Version() bool { return goversion.Version > 0 }
`)}}
	out, stderr, err := runS270G1i2Linked(t, "example.com/s270", `package main
import (
	"fmt"
	p "cmd/s270g1i2"
)
func main() { fmt.Println(p.Version()) }
`, pkg)
	if err != nil || out != "true\n" {
		t.Fatalf("err=%v stdout=%q stderr=%q", err, out, stderr)
	}
}

// A body-less linked-package function whose //go:linkname pulls a symbol of
// that same interpreted package has no native definition to link
// (go/types badlinkname_Checker_infer).
func TestGoSourceS270G1i2LinknamePullOfInterpretedSymbol(t *testing.T) {
	pkg := gosource.PackageSpec{Path: "example.com/s270/p", Sources: []gosource.Source{s249Source("p.go", `package p

import (
	"fmt"
	_ "unsafe"
)

type Checker struct{ n int }

func (c *Checker) infer() int { return c.n }

// infer should be an internal detail.
//
//go:linkname badlinkname_Checker_infer example.com/s270/p.(*Checker).infer
func badlinkname_Checker_infer(*Checker) int

func Run() { fmt.Println((&Checker{n: 3}).infer()) }
`)}}
	out, stderr, err := runS270G1i2Linked(t, "example.com/s270", `package main
import "example.com/s270/p"
func main() { p.Run() }
`, pkg)
	if err != nil || out != "3\n" {
		t.Fatalf("err=%v stdout=%q stderr=%q", err, out, stderr)
	}
}
