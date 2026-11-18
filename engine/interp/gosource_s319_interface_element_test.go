package interp_test

// Sprint: #319; Story: #1086; Story-ID: 2405313cf7e2

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

func runS319Source(t *testing.T, name, source string) (string, string, error) {
	t.Helper()
	program, err := gosource.Parse(strings.NewReader(source), name+".go", gosource.Options{RunMain: true})
	if err != nil {
		t.Fatalf("gosource rejected the source: %v", err)
	}
	var out, errout bytes.Buffer
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(t.TempDir()), interp.StdIO(nil, &out, &errout))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	err = runner.Run(ctx, program.File)
	return out.String(), errout.String(), err
}

// An element of a dependency-owned slice of interface type read by index is
// an interface value over the dependency's dynamic value, exactly as the same
// element binds in a range: assigning it to an interface variable must not
// report BASHPP-EINTERFACE-VALUE (cmd/compile/internal/types2 resolver.go:364,
// init = values[i] with values []syntax.Expr from syntax.UnpackListExpr).
func TestGoSourceS319NativeInterfaceElementIndex(t *testing.T) {
	out, stderr, err := runS319Source(t, "s319-native-iface-index", `package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
)

func main() {
	f, err := parser.ParseFile(token.NewFileSet(), "x.go", "package p\nconst a = 1\nfunc F() {}\n", 0)
	if err != nil {
		panic(err)
	}
	decls := f.Decls
	for i := range 3 {
		var init ast.Decl
		if i < len(decls) {
			init = decls[i]
		}
		if init == nil {
			fmt.Println(i, "nil")
			continue
		}
		_, isFunc := init.(*ast.FuncDecl)
		fmt.Println(i, isFunc)
	}
}`)
	if err != nil || out != "0 false\n1 true\n2 nil\n" {
		t.Fatalf("err=%v stdout=%q stderr=%q", err, out, stderr)
	}
}

// The indexed element carries its dynamic type into a type switch, and it
// crosses back into a dependency call (fmt's %T names the dynamic type).
func TestGoSourceS319NativeInterfaceElementTypeSwitch(t *testing.T) {
	out, stderr, err := runS319Source(t, "s319-native-iface-switch", `package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
)

func main() {
	f, err := parser.ParseFile(token.NewFileSet(), "x.go", "package p\nconst a = 1\nfunc F() {}\n", 0)
	if err != nil {
		panic(err)
	}
	decls := f.Decls
	switch d := decls[0].(type) {
	case *ast.GenDecl:
		fmt.Println("gen", len(d.Specs))
	default:
		fmt.Println("other")
	}
	fmt.Printf("%T\n", decls[1])
}`)
	if err != nil || out != "gen 1\n*ast.FuncDecl\n" {
		t.Fatalf("err=%v stdout=%q stderr=%q", err, out, stderr)
	}
}

// A dependency-owned struct member of interface type reads back as an
// interface value too, both through a named local and through a computed base.
func TestGoSourceS319NativeInterfaceMember(t *testing.T) {
	out, stderr, err := runS319Source(t, "s319-native-iface-member", `package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
)

func main() {
	f, err := parser.ParseFile(token.NewFileSet(), "x.go", "package p\nvar a = 1 + 2\n", 0)
	if err != nil {
		panic(err)
	}
	vs := f.Decls[0].(*ast.GenDecl).Specs[0].(*ast.ValueSpec)
	be := vs.Values[0].(*ast.BinaryExpr)
	var e ast.Expr = be.X
	lit, ok := e.(*ast.BasicLit)
	fmt.Println(ok, lit.Value)
	var y ast.Expr = vs.Values[0].(*ast.BinaryExpr).Y
	fmt.Printf("%T\n", y)
}`)
	if err != nil || out != "true 1\n*ast.BasicLit\n" {
		t.Fatalf("err=%v stdout=%q stderr=%q", err, out, stderr)
	}
}

// Ranging the same dependency-owned interface slice keeps working: the range
// path is the one the indexed read now matches.
func TestGoSourceS319NativeInterfaceElementRange(t *testing.T) {
	out, stderr, err := runS319Source(t, "s319-native-iface-range", `package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
)

func main() {
	f, err := parser.ParseFile(token.NewFileSet(), "x.go", "package p\nconst a = 1\nfunc F() {}\n", 0)
	if err != nil {
		panic(err)
	}
	kinds := ""
	for _, d := range f.Decls {
		if _, ok := d.(*ast.FuncDecl); ok {
			kinds += "F"
		} else {
			kinds += "G"
		}
	}
	fmt.Println(kinds)
}`)
	if err != nil || out != "GF\n" {
		t.Fatalf("err=%v stdout=%q stderr=%q", err, out, stderr)
	}
}
