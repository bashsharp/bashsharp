//go:build full

package interp_test

// Sprint: #281; Story: #809; Story-ID: fac7e14af4a8

import "testing"

func TestGoTypesParserCheckNativeEquivalence(t *testing.T) {
	const source = `package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
)

const src = "package p\nconst C = 1\nfunc F() {}\n"

func main() {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "p.go", src, parser.SkipObjectResolution)
	if err != nil {
		panic(err)
	}
	conf := types.Config{}
	if _, err := conf.Check("p", fset, []*ast.File{file}, nil); err != nil {
		panic(err)
	}
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.GenDecl:
			fmt.Println("gen", d.Tok == token.CONST, len(d.Specs))
		case *ast.FuncDecl:
			fmt.Println("func", d.Name.Name)
		case ast.Decl:
			fmt.Println("iface")
		default:
			fmt.Printf("default %T\n", d)
		}
	}
	var first ast.Decl = file.Decls[0]
	switch first.(type) {
	case ast.Decl:
		fmt.Println("interface")
	default:
		fmt.Println("interface-miss")
	}
	var none ast.Decl
	switch none.(type) {
	case nil:
		fmt.Println("nil")
	default:
		fmt.Println("nil-miss")
	}
	var other ast.Decl = &ast.FuncDecl{}
	switch other.(type) {
	case *ast.GenDecl:
		fmt.Println("wrong")
	default:
		fmt.Println("negative")
	}
}
`
	got, err := runGoSourceIdentity(t, source, "")
	if err != nil {
		t.Fatalf("Runner: %v; outcome=%+v", err, got)
	}
	const want = "gen true 1\nfunc F\ninterface\nnil\nnegative\n"
	if got.stdout != want || got.stderr != "" || got.status != 0 {
		t.Fatalf("outcome=%+v; want stdout %q", got, want)
	}
}
