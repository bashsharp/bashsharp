//go:build full

package interp_test

// Sprint: #281; Story: #809; Story-ID: fac7e14af4a8
//
// Interpreted go/types' TestTypeSetString died in resolver.go's
// packageObjects at
//
//	if check.objMap[tname].tdecl.Assign.IsValid() {
//
// with BASHPP-EEXPR-UNDEFINED: undefined callable computed function. Assign is
// a token.Pos field of a dependency-owned *ast.TypeSpec and IsValid is the
// dependency's method, but the chain to that receiver crosses the
// interpreter's own storage (a map entry holding a *declInfo). The runtime
// walker that reads such a field only handles a selector path rooted at a
// name, and the static walk had no field table for an imported struct, so the
// receiver was classified as interpreter-owned and no callable was found. The
// same call spelled through a named local — `info := check.objMap[tname];
// info.tdecl.Assign.IsValid()` — worked, which is what made the gap a spelling
// accident rather than a missing capability.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"testing"
)

// s809AliasSource declares a plain type and an alias, so Assign is both the
// zero position and a real one.
const s809AliasSource = "package p; type T interface{ m() }\ntype B = T\n"

// The reduction of resolver.go's packageObjects: a method of a native scalar
// field reached through an interpreted map entry, called both as an `if`
// condition (the scalar evaluator's path, which is where the leaf failed) and
// as a call argument (the general expression path).
func TestS809NativeFieldMethodThroughInterpretedStorage(t *testing.T) {
	const source = `package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
)

type declInfo struct{ tdecl *ast.TypeSpec }

type Checker struct{ objMap map[string]*declInfo }

func main() {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "p.go", "package p; type T interface{ m() }\ntype B = T\n", 0)
	if err != nil {
		panic(err)
	}
	check := &Checker{objMap: make(map[string]*declInfo)}
	for _, decl := range file.Decls {
		gen := decl.(*ast.GenDecl)
		spec := gen.Specs[0].(*ast.TypeSpec)
		check.objMap[spec.Name.Name] = &declInfo{tdecl: spec}
	}
	for _, name := range []string{"T", "B"} {
		if check.objMap[name].tdecl.Assign.IsValid() {
			fmt.Println(name, "alias")
		} else {
			fmt.Println(name, "decl")
		}
		fmt.Println(name, check.objMap[name].tdecl.Assign.IsValid())
		info := check.objMap[name]
		fmt.Println(name, info.tdecl.Assign.IsValid())
	}
}
`
	got, err := runGoSourceIdentity(t, source, "")
	if err != nil {
		t.Fatalf("Runner: %v; outcome=%+v", err, got)
	}
	want := s809NativeAliasReport(t)
	if got.stdout != want || got.stderr != "" || got.status != 0 {
		t.Fatalf("outcome=%+v; want stdout %q", got, want)
	}
}

// s809NativeAliasReport is the same report computed by this test binary over
// the same go/parser and go/ast: the oracle is Go's own answer, not a pinned
// string.
func s809NativeAliasReport(t *testing.T) string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "p.go", s809AliasSource, 0)
	if err != nil {
		t.Fatal(err)
	}
	specs := make(map[string]*ast.TypeSpec)
	for _, decl := range file.Decls {
		spec := decl.(*ast.GenDecl).Specs[0].(*ast.TypeSpec)
		specs[spec.Name.Name] = spec
	}
	var out strings.Builder
	for _, name := range []string{"T", "B"} {
		valid := specs[name].Assign.IsValid()
		if valid {
			fmt.Fprintln(&out, name, "alias")
		} else {
			fmt.Fprintln(&out, name, "decl")
		}
		fmt.Fprintln(&out, name, valid)
		fmt.Fprintln(&out, name, valid)
	}
	return out.String()
}

// s809TypeSetBodies are TestTypeSetString's own interface bodies, including
// the declared constraint that resolves comparable through the universe while
// checking E.
var s809TypeSetBodies = []string{"{}", "{int}", "{m()}", "{comparable}", "{error}", "{m(); comparable; int|float32|string}", "{E}; type E interface{comparable}"}

// The same chain over a field whose declared type is the dependency's own
// struct rather than a scalar: `tdecl.Name.NamePos.IsValid()` crosses two
// native selectors after the interpreted map entry.
func TestS809NativeNestedFieldMethodThroughInterpretedStorage(t *testing.T) {
	const source = `package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
)

type declInfo struct{ tdecl *ast.TypeSpec }

func main() {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "p.go", "package p; type T interface{ m() }\ntype B = T\n", 0)
	if err != nil {
		panic(err)
	}
	infos := make(map[string]*declInfo)
	for _, decl := range file.Decls {
		gen := decl.(*ast.GenDecl)
		spec := gen.Specs[0].(*ast.TypeSpec)
		infos[spec.Name.Name] = &declInfo{tdecl: spec}
	}
	for _, name := range []string{"T", "B"} {
		fmt.Println(name, infos[name].tdecl.Name.NamePos.IsValid(), infos[name].tdecl.Name.Name)
	}
}
`
	got, err := runGoSourceIdentity(t, source, "")
	if err != nil {
		t.Fatalf("Runner: %v; outcome=%+v", err, got)
	}
	want := s809NativeNestedReport(t)
	if got.stdout != want || got.stderr != "" || got.status != 0 {
		t.Fatalf("outcome=%+v; want stdout %q", got, want)
	}
}

// s809NativeNestedReport is that nested read computed natively.
func s809NativeNestedReport(t *testing.T) string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "p.go", s809AliasSource, 0)
	if err != nil {
		t.Fatal(err)
	}
	specs := make(map[string]*ast.TypeSpec)
	for _, decl := range file.Decls {
		spec := decl.(*ast.GenDecl).Specs[0].(*ast.TypeSpec)
		specs[spec.Name.Name] = spec
	}
	var out strings.Builder
	for _, name := range []string{"T", "B"} {
		fmt.Fprintln(&out, name, specs[name].Name.NamePos.IsValid(), specs[name].Name.Name)
	}
	return out.String()
}

// TestTypeSetString's bodies checked end to end, so the construct the leaf
// reported is exercised over the same inputs the upstream test uses.
func TestS809GoTypesTypeSetStringBodies(t *testing.T) {
	const source = `package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
)

func main() {
	for _, body := range []string{"{}", "{int}", "{m()}", "{comparable}", "{error}", "{m(); comparable; int|float32|string}", "{E}; type E interface{comparable}"} {
		src := "package p; type T interface" + body
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, "p.go", src, parser.AllErrors|parser.SkipObjectResolution)
		if err != nil {
			panic(err)
		}
		var conf types.Config
		pkg, err := conf.Check(file.Name.Name, fset, []*ast.File{file}, nil)
		if err != nil {
			panic(err)
		}
		obj := pkg.Scope().Lookup("T")
		T, ok := obj.Type().Underlying().(*types.Interface)
		if !ok {
			panic("not an interface")
		}
		fmt.Println(body, T.String(), T.NumMethods(), T.IsComparable(), T.IsMethodSet())
	}
}
`
	got, err := runGoSourceIdentity(t, source, "")
	if err != nil {
		t.Fatalf("Runner: %v; outcome=%+v", err, got)
	}
	want := s809NativeTypeSetStringReport(t)
	if got.stdout != want || got.stderr != "" || got.status != 0 {
		t.Fatalf("outcome=%+v; want stdout %q", got, want)
	}

	// The imported go/types call above is native. Keep a mapped-package
	// reduction of the interpreted package path too: go/types declares
	// TypeName in object.go, constructs the universe object in universe.go,
	// and asserts it while resolving the declaration from typexpr.go. The
	// final body is the first TestTypeSetString case to take all three steps.
	out, stderr, err := runGoSourcePackages(t, `package main
import typeset "test/p"
func main() { typeset.Check("{E}; type E interface{comparable}") }
`, map[string]string{
		"a.go": `package typeset
import "go/ast"
type Type interface { Underlying() Type }
type Object interface { Name() string; Type() Type }
type object struct { name string; typ Type }
func (obj *object) Name() string { return obj.name }
func (obj *object) Type() Type { return obj.typ }
type TypeName struct { object }
type Named struct { obj *TypeName; fromRHS Type }
func (typ *Named) Underlying() Type { return typ.fromRHS }
func NewTypeName(name string) *TypeName { return &TypeName{object: object{name: name}} }
func NewNamed(obj *TypeName, rhs Type) *Named {
	typ := &Named{obj: obj, fromRHS: rhs}
	obj.typ = typ
	return typ
}
type Scope struct { elems map[string]Object }
func NewScope() *Scope { return &Scope{elems: make(map[string]Object)} }
func (s *Scope) Insert(obj Object) { s.elems[obj.Name()] = obj }
func resolve(name string, obj Object) Object { return obj }
func (s *Scope) Lookup(name string) Object { return resolve(name, s.elems[name]) }
type declInfo struct { tdecl *ast.TypeSpec }
var Universe = NewScope()
var universeComparable Object
func init() {
	obj := NewTypeName("comparable")
	NewNamed(obj, nil)
	Universe.Insert(obj)
	universeComparable = Universe.Lookup("comparable")
}
type Checker struct {
	objMap map[*TypeName]*declInfo
	objects []*TypeName
}
`,
		"b.go": `package typeset
import (
	"go/ast"
	"go/parser"
	"go/token"
)
func Check(body string) {
	src := "package p; type T interface" + body
	file, err := parser.ParseFile(token.NewFileSet(), "p.go", src, parser.AllErrors|parser.SkipObjectResolution)
	if err != nil { panic(err) }
	check := &Checker{objMap: make(map[*TypeName]*declInfo)}
	var tdecl *ast.TypeSpec
	for _, decl := range file.Decls {
		gen := decl.(*ast.GenDecl)
		for _, spec := range gen.Specs {
			tspec := spec.(*ast.TypeSpec)
			obj := NewTypeName(tspec.Name.Name)
			NewNamed(obj, nil)
			check.objects = append(check.objects, obj)
			check.objMap[obj] = &declInfo{tdecl: tspec}
			if obj.Name() == "T" { tdecl = tspec }
		}
	}
	check.report(tdecl.Type)
}
`,
		"c.go": `package typeset
import (
	"fmt"
	"go/ast"
)
func (check *Checker) lookup(name string) Object {
	if obj := Universe.Lookup(name); obj != nil { return obj }
	for _, obj := range check.objects {
		if obj.Name() == name { return obj }
	}
	return nil
}
func (check *Checker) typ(expr ast.Expr) bool {
	switch expr := expr.(type) {
	case *ast.Ident:
		obj := check.lookup(expr.Name)
		predeclared := obj == universeComparable
		_, gotType := obj.(*TypeName)
		_, gotNamed := obj.Type().(*Named)
		if !gotType || !gotNamed { return false }
		if predeclared {
			return true
		}
		return check.objDecl(obj)
	case *ast.InterfaceType:
		for _, field := range expr.Methods.List {
			if !check.parseUnion(field.Type) { return false }
		}
		return true
	}
	return false
}
func flattenUnion(list []ast.Expr, expr ast.Expr) []ast.Expr { return append(list, expr) }
func (check *Checker) parseTilde(expr ast.Expr) bool {
	x := expr
	if unary, _ := x.(*ast.UnaryExpr); unary != nil { x = unary.X }
	return check.typ(x)
}
func (check *Checker) parseUnion(expr ast.Expr) bool {
	for _, term := range flattenUnion(nil, expr) {
		if !check.parseTilde(term) { return false }
	}
	return true
}
func (check *Checker) objDecl(obj Object) bool {
	tname := obj.(*TypeName)
	info := check.objMap[tname]
	return check.typ(info.tdecl.Type)
}
func (check *Checker) report(expr ast.Expr) { fmt.Println(check.typ(expr)) }
`,
	})
	if err != nil || out != "true\n" || stderr != "" {
		t.Fatalf("interpreted universe reduction: err=%v stdout=%q stderr=%q", err, out, stderr)
	}
}

// s809NativeTypeSetStringReport runs the same bodies through go/types here.
func s809NativeTypeSetStringReport(t *testing.T) string {
	t.Helper()
	var out strings.Builder
	for _, body := range s809TypeSetBodies {
		src := "package p; type T interface" + body
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, "p.go", src, parser.AllErrors|parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		var conf types.Config
		pkg, err := conf.Check(file.Name.Name, fset, []*ast.File{file}, nil)
		if err != nil {
			t.Fatal(err)
		}
		obj := pkg.Scope().Lookup("T")
		T, ok := obj.Type().Underlying().(*types.Interface)
		if !ok {
			t.Fatalf("%s: not an interface", body)
		}
		fmt.Fprintln(&out, body, T.String(), T.NumMethods(), T.IsComparable(), T.IsMethodSet())
	}
	return out.String()
}
