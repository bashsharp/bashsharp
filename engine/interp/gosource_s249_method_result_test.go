//go:build full

package interp_test

import "testing"

// Sprint: #249; Story: #714; Story-ID: 5251dc793b6e
//
// A struct value held by an `error` interface is never equal to nil. The
// dependency worker compares the dynamic value it is handed; the interface
// marker on that value, not its dynamic kind, decides the nil comparison.
func TestGoSourceS249StructErrorInterfaceNilComparison(t *testing.T) {
	typedSendThreeModes(t, `package main

import "go/scanner"

func main() {
	var err error = scanner.Error{Msg: "boom"}
	println(err == nil, err != nil, nil == err)
	got, ok := err.(scanner.Error)
	println(ok, got.Msg)
}
`)
}

// An imported pointer-receiver method on an addressable dot-imported struct
// returns a concrete struct through an `error` result. Re-assigning that
// result into an already declared `error` variable, returning it through an
// interpreted `error` result, comparing it with nil, and asserting it against
// the dot-imported and the package-qualified struct type must all observe the
// interface wrapper.
func TestGoSourceS249DotImportedMethodResultKeepsInterface(t *testing.T) {
	typedSendThreeModes(t, `package main

import (
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	. "go/types"
	"reflect"
)

const example = "package p\n\nvar _ = missing\n"

func check() error {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "example.go", example, parser.SkipObjectResolution)
	if err != nil {
		return err
	}
	conf := Config{FakeImportC: true, Importer: importer.Default()}
	_, err = conf.Check("example", fset, []*ast.File{file}, nil)
	return err
}

func main() {
	err := check()
	if err == nil {
		panic("no error")
	}
	dotted, ok := err.(Error)
	if !ok {
		panic("lost interface (dot import)")
	}
	named, ok := err.(types.Error)
	if !ok {
		panic("lost interface (named import)")
	}
	println(dotted.Msg == named.Msg, reflect.ValueOf(err).FieldByName("Soft").Bool())
}
`)
}

// Ranging over a dependency-owned sequence whose element type is an interface
// binds each element as an interface value: the loop variable is a valid type
// assertion and type switch operand, a shadowing `x, ok := x.(T)` sees the
// interface operand, and both a nil element and a scalar-backed element keep
// their dynamic type.
func TestGoSourceS249RangeNativeInterfaceElements(t *testing.T) {
	typedSendThreeModes(t, `package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
)

func main() {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "x.go", "package x\n\nimport \"fmt\"\n\nconst A, B = 1, 2\n\nvar V int\n\nfunc F() { fmt.Println() }\n", parser.SkipObjectResolution)
	if err != nil {
		panic(err)
	}
	for i, decl := range file.Decls {
		decl, ok := decl.(*ast.GenDecl)
		if !ok {
			fmt.Println(i, "not a GenDecl")
			continue
		}
		for _, spec := range decl.Specs {
			switch spec := spec.(type) {
			case *ast.ValueSpec:
				fmt.Println(i, decl.Tok, len(spec.Names))
			case *ast.ImportSpec:
				fmt.Println(i, decl.Tok, spec.Path.Value)
			}
		}
	}
	values := []any{1, "two", nil, 3.5, []int{4}}
	for _, v := range values {
		switch v := v.(type) {
		case int:
			fmt.Println("int", v+1)
		case string:
			fmt.Println("string", len(v))
		case nil:
			fmt.Println("nil", v == nil)
		default:
			fmt.Printf("%T\n", v)
		}
	}
	keys := map[string]any{"a": 1, "b": "x"}
	names := make([]string, 0, len(keys))
	for k, v := range keys {
		if n, ok := v.(int); ok {
			names = append(names, fmt.Sprint(k, n))
		} else {
			names = append(names, k)
		}
	}
	sort.Strings(names)
	fmt.Println(names)
}
`)
}
