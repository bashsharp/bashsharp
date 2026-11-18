//go:build full

package interp_test

// Sprint: #319; Story: #1086; Story-ID: 2405313cf7e2

import "testing"

// types2 collectObjects declares `var last *syntax.ConstDecl`, reassigns it to
// new(syntax.ConstDecl), and later resets it with `last = nil`. The variable's
// declared type must survive the reassignment to interpreter storage, or the
// untyped nil has no type to take (BASHPP-EASSIGN-UNDECLARED: RHS nil).
func TestS319DependencyPointerVarKeepsDeclaredTypeAcrossNil(t *testing.T) {
	const source = `package main

import (
	"fmt"
	"go/ast"
)

func main() {
	var last *ast.GenDecl
	last = new(ast.GenDecl)
	last = nil
	fmt.Println(last == nil)

	decls := []ast.Decl{&ast.GenDecl{}, &ast.BadDecl{}, &ast.GenDecl{Specs: []ast.Spec{&ast.ValueSpec{}}}}
	first := -1
	var prev *ast.GenDecl
	for index, decl := range decls {
		if _, ok := decl.(*ast.GenDecl); !ok {
			first = -1
		}
		switch s := decl.(type) {
		case *ast.GenDecl:
			if first < 0 {
				first = index
				prev = nil
			}
			switch {
			case s.Specs != nil:
				prev = s
			case prev == nil:
				prev = new(ast.GenDecl)
			}
			fmt.Println(index, first, len(prev.Specs))
		}
	}
}
`
	got, err := runGoSourceIdentity(t, source, "")
	if err != nil || got.stdout != "true\n0 0 0\n2 2 1\n" || got.stderr != "" || got.status != 0 {
		t.Fatalf("outcome=%+v; err=%v", got, err)
	}
}

// types2 decides constant-group membership with
// `file.DeclList[index-1].(*syntax.ConstDecl).Group != s.Group`. A selector on
// a type assertion to a dependency type is the dependency's value: it must
// compare by pointer identity, not by the handle's wire text. When it compared
// unequal, every inherited constant lost its init expression ("missing init
// expr for b" in TestValuesInfo).
func TestS319DependencyTypeAssertSelectorComparesIdentity(t *testing.T) {
	const source = `package main

import (
	"fmt"
	"go/ast"
)

func main() {
	g := &ast.CommentGroup{}
	a := &ast.ValueSpec{Doc: g, Values: []ast.Expr{&ast.Ident{Name: "iota"}}}
	b := &ast.ValueSpec{Doc: g}
	specs := []ast.Spec{a, b}
	var one ast.Spec = b
	fmt.Println(specs[0].(*ast.ValueSpec).Doc == g, one.(*ast.ValueSpec).Doc == g)
	fmt.Println(specs[0].(*ast.ValueSpec).Doc == one.(*ast.ValueSpec).Doc, specs[0].(*ast.ValueSpec).Doc != g)
	fmt.Println(specs[1].(*ast.ValueSpec).Doc == nil, specs[0].(*ast.ValueSpec).Values[0].(*ast.Ident).Name)

	first := -1
	var last *ast.ValueSpec
	for index, spec := range specs {
		switch s := spec.(type) {
		case *ast.ValueSpec:
			if first < 0 || s.Doc == nil || specs[index-1].(*ast.ValueSpec).Doc != s.Doc {
				first = index
				last = nil
			}
			switch {
			case s.Values != nil:
				last = s
			case last == nil:
				last = new(ast.ValueSpec)
			}
			fmt.Println(index, first, len(last.Values))
		}
	}
}
`
	got, err := runGoSourceIdentity(t, source, "")
	want := "true true\ntrue false\nfalse iota\n0 0 1\n1 0 1\n"
	if err != nil || got.stdout != want || got.stderr != "" || got.status != 0 {
		t.Fatalf("outcome=%+v; err=%v\nwant stdout %q", got, err, want)
	}
}
