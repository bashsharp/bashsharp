//go:build full

package interp_test

// Sprint: #281; Story: #809; Story-ID: fac7e14af4a8
//
// Interpreted go/types' TestTypeSetString died in cycles.go's directCycle at
//
//	rhs, ok := check.objMap[tname].tdecl.Type.(*ast.Ident)
//
// with BASHPP-EASSERT-OPERAND: the operand reads an interface-typed field of a
// dependency-owned *ast.TypeSpec, but the path to that handle crosses the
// interpreter's own storage (a map entry holding a *declInfo), so the native
// reader could not claim the whole expression and the generic reader returned
// the worker's bare handle without interface metadata. The same read spelled
// through a named local — `info := check.objMap[tname]; info.tdecl.Type` —
// worked, which is what made the gap a spelling accident rather than a
// missing capability.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"testing"
)

// s809CycleSource declares an interface literal RHS, an identifier RHS and an
// alias, so the assertion under test both succeeds and fails, and the type
// switch over the same operand selects both arms.
const s809CycleSource = "package p; type T interface{ m() }\ntype A T\ntype B = A\n"

// The reduction of cycles.go's directCycle: the interface-typed field of a
// native *ast.TypeSpec must stay interface-typed when the chain to it runs
// through an interpreted map entry or slice element, so `x.(*ast.Ident)` and
// `switch x.(type)` over it are legal.
func TestS809NativeInterfaceFieldThroughInterpretedStorage(t *testing.T) {
	const source = `package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
)

type Object interface{ Name() string }

type TypeName struct{ name string }

func (t *TypeName) Name() string { return t.name }

type declInfo struct{ tdecl *ast.TypeSpec }

type Checker struct {
	objMap  map[Object]*declInfo
	objList []Object
	infos   []*declInfo
}

// report is directCycle's read, once through the map keyed by the interface
// and once through the slice of declaration infos.
func (check *Checker) report() {
	for i, obj := range check.objList {
		tname, ok := obj.(*TypeName)
		if !ok {
			continue
		}
		rhs, ok := check.objMap[tname].tdecl.Type.(*ast.Ident)
		if ok {
			fmt.Println("map", tname.Name(), rhs.Name)
		} else {
			fmt.Println("map", tname.Name(), "literal")
		}
		switch typ := check.infos[i].tdecl.Type.(type) {
		case *ast.Ident:
			fmt.Println("slice", tname.Name(), typ.Name)
		default:
			fmt.Println("slice", tname.Name(), "literal")
		}
	}
}

func main() {
	src := "package p; type T interface{ m() }\ntype A T\ntype B = A\n"
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "p.go", src, 0)
	if err != nil {
		panic(err)
	}
	check := &Checker{objMap: make(map[Object]*declInfo)}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range gen.Specs {
			tspec, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			obj := &TypeName{name: tspec.Name.Name}
			info := &declInfo{tdecl: tspec}
			check.objMap[obj] = info
			check.objList = append(check.objList, obj)
			check.infos = append(check.infos, info)
		}
	}
	check.report()
}
`
	got, err := runGoSourceIdentity(t, source, "")
	if err != nil {
		t.Fatalf("Runner: %v; outcome=%+v", err, got)
	}
	want := s809NativeCycleReport(t)
	if got.stdout != want || got.stderr != "" || got.status != 0 {
		t.Fatalf("outcome=%+v; want stdout %q", got, want)
	}
}

// s809NativeCycleReport is the interpreted program's report computed by this
// test binary itself, over the same go/parser and go/ast: the oracle is Go's
// own answer, not a pinned string.
func s809NativeCycleReport(t *testing.T) string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "p.go", s809CycleSource, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range gen.Specs {
			tspec, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			name := "literal"
			if rhs, ok := tspec.Type.(*ast.Ident); ok {
				name = rhs.Name
			}
			fmt.Fprintf(&out, "map %s %s\n", tspec.Name.Name, name)
			fmt.Fprintf(&out, "slice %s %s\n", tspec.Name.Name, name)
		}
	}
	return out.String()
}

// TestTypeSetString's own shape: check a one-line package, look T up in the
// package scope and assert its underlying type to *types.Interface, then
// report the type set's facts. The assertion operand is the result of a chain
// of native calls, which is the other spelling that has to keep its interface.
func TestS809GoTypesTypeSetShape(t *testing.T) {
	const source = `package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
)

func main() {
	for _, body := range []string{"{}", "{int}", "{m()}", "{comparable}", "{error}"} {
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
		if obj == nil {
			panic("T not found")
		}
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
	want := s809NativeTypeSetReport(t)
	if got.stdout != want || got.stderr != "" || got.status != 0 {
		t.Fatalf("outcome=%+v; want stdout %q", got, want)
	}
}

// s809NativeTypeSetReport runs TestTypeSetString's shape natively over the
// same bodies, so the interpreted run is compared with go/types' own answer.
func s809NativeTypeSetReport(t *testing.T) string {
	t.Helper()
	var out strings.Builder
	for _, body := range []string{"{}", "{int}", "{m()}", "{comparable}", "{error}"} {
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
		if obj == nil {
			t.Fatalf("%s: T not found", body)
		}
		T, ok := obj.Type().Underlying().(*types.Interface)
		if !ok {
			t.Fatalf("%s: not an interface", body)
		}
		fmt.Fprintln(&out, body, T.String(), T.NumMethods(), T.IsComparable(), T.IsMethodSet())
	}
	return out.String()
}

// s809ArraySource pairs a slice type, whose *ast.ArrayType.Len is the nil
// Expr, with an array type whose Len is a literal.
const s809ArraySource = "package p; type S []int\ntype A [3]int\n"

// The same chain over a field that is the nil interface: the assertion must
// report false rather than a match, and the type switch must select `case nil`
// — a handle that arrives interface-typed must still arrive nil.
func TestS809NativeNilInterfaceFieldThroughInterpretedStorage(t *testing.T) {
	const source = `package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
)

type declInfo struct{ tdecl *ast.TypeSpec }

type Checker struct{ infos map[string]*declInfo }

func main() {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "p.go", "package p; type S []int\ntype A [3]int\n", 0)
	if err != nil {
		panic(err)
	}
	check := &Checker{infos: make(map[string]*declInfo)}
	for _, decl := range file.Decls {
		gen := decl.(*ast.GenDecl)
		spec := gen.Specs[0].(*ast.TypeSpec)
		check.infos[spec.Name.Name] = &declInfo{tdecl: spec}
	}
	for _, name := range []string{"S", "A"} {
		arr := check.infos[name].tdecl.Type.(*ast.ArrayType)
		lit, ok := arr.Len.(*ast.BasicLit)
		fmt.Println(name, ok, lit != nil)
		switch check.infos[name].tdecl.Type.(*ast.ArrayType).Len.(type) {
		case nil:
			fmt.Println(name, "nil")
		case *ast.BasicLit:
			fmt.Println(name, "lit")
		default:
			fmt.Println(name, "other")
		}
	}
}
`
	got, err := runGoSourceIdentity(t, source, "")
	if err != nil {
		t.Fatalf("Runner: %v; outcome=%+v", err, got)
	}
	want := s809NativeArrayReport(t)
	if got.stdout != want || got.stderr != "" || got.status != 0 {
		t.Fatalf("outcome=%+v; want stdout %q", got, want)
	}
}

// s809NativeArrayReport is the array-length report this test binary computes
// natively over the same sources.
func s809NativeArrayReport(t *testing.T) string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "p.go", s809ArraySource, 0)
	if err != nil {
		t.Fatal(err)
	}
	specs := make(map[string]*ast.TypeSpec)
	for _, decl := range file.Decls {
		spec := decl.(*ast.GenDecl).Specs[0].(*ast.TypeSpec)
		specs[spec.Name.Name] = spec
	}
	var out strings.Builder
	for _, name := range []string{"S", "A"} {
		arr := specs[name].Type.(*ast.ArrayType)
		lit, ok := arr.Len.(*ast.BasicLit)
		fmt.Fprintln(&out, name, ok, lit != nil)
		switch arr.Len.(type) {
		case nil:
			fmt.Fprintln(&out, name, "nil")
		case *ast.BasicLit:
			fmt.Fprintln(&out, name, "lit")
		default:
			fmt.Fprintln(&out, name, "other")
		}
	}
	return out.String()
}
