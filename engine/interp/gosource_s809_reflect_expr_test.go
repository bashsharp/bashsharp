//go:build full

package interp_test

// Sprint: #281; Story: #809; Story-ID: fac7e14af4a8

import (
	"os"
	"path/filepath"
	"testing"
)

// Minimal reduction of cmd/compile/internal/syntax's Fdump walking its own AST:
// grouped type declarations whose structs end in an embedded unexported struct,
// with sibling shapes where field 0 is a string in one (Name) and an interface
// in another (ParenExpr, CallExpr), reached through []Decl / []Expr slots.
func TestS809ReflectInterpretedExprGraph(t *testing.T) {
	const dependency = `package tree

import (
	"fmt"
	"reflect"
)

type Pos uint32

type LitKind uint8

type Node interface {
	Pos() Pos
	aNode()
}

type node struct {
	pos Pos
}

func (n *node) Pos() Pos { return n.pos }
func (*node) aNode()     {}

type File struct {
	PkgName   *Name
	DeclList  []Decl
	GoVersion string
	node
}

type (
	Decl interface {
		Node
		aDecl()
	}

	VarDecl struct {
		NameList []*Name
		Values   Expr
		decl
	}
)

type decl struct{ node }

func (*decl) aDecl() {}

type (
	Expr interface {
		Node
		aExpr()
	}

	Name struct {
		Value string
		expr
	}

	BasicLit struct {
		Value string
		Kind  LitKind
		Bad   bool
		expr
	}

	ParenExpr struct {
		X Expr
		expr
	}

	CallExpr struct {
		Fun     Expr
		ArgList []Expr
		HasDots bool
		expr
	}
)

type expr struct{ node }

func (*expr) aExpr() {}

type dumper struct {
	ptrmap map[Node]int
	next   int
}

func (p *dumper) dump(x reflect.Value, n Node) {
	switch x.Kind() {
	case reflect.Interface:
		if x.IsNil() {
			fmt.Print("nil")
			return
		}
		p.dump(x.Elem(), nil)
	case reflect.Ptr:
		if x.IsNil() {
			fmt.Print("nil")
			return
		}
		if x, ok := x.Interface().(*Name); ok {
			fmt.Printf("%s @ %v", x.Value, x.Pos())
			return
		}
		fmt.Print("*")
		if ptr, ok := x.Interface().(Node); ok {
			if id, seen := p.ptrmap[ptr]; seen {
				fmt.Printf("(Node @ %d)", id)
				return
			}
			p.next++
			p.ptrmap[ptr] = p.next
			n = ptr
		}
		p.dump(x.Elem(), n)
	case reflect.Slice:
		if x.IsNil() {
			fmt.Print("nil")
			return
		}
		fmt.Printf("%s (%d entries) {", x.Type(), x.Len())
		for i, m := 0, x.Len(); i < m; i++ {
			fmt.Printf("%d: ", i)
			p.dump(x.Index(i), nil)
			fmt.Print(" ")
		}
		fmt.Print("}")
	case reflect.Struct:
		typ := x.Type()
		fmt.Printf("%s {", typ)
		for i, m := 0, typ.NumField(); i < m; i++ {
			name := typ.Field(i).Name
			if name[0] < 'A' || name[0] > 'Z' {
				continue
			}
			fmt.Printf("%s: ", name)
			p.dump(x.Field(i), nil)
			fmt.Print(" ")
		}
		fmt.Print("}")
	default:
		switch x := x.Interface().(type) {
		case string:
			fmt.Printf("%q", x)
		default:
			fmt.Printf("%v", x)
		}
	}
}

func Fdump(n Node) {
	p := dumper{ptrmap: make(map[Node]int)}
	p.dump(reflect.ValueOf(n), n)
	fmt.Println()
}

func Sample() *File {
	pkg := &Name{Value: "main"}
	call := &CallExpr{
		Fun:     &ParenExpr{X: &Name{Value: "println"}},
		ArgList: []Expr{&BasicLit{Value: "42", Kind: 7}, &Name{Value: "x"}},
	}
	return &File{
		PkgName:   pkg,
		GoVersion: "go1.27",
		DeclList: []Decl{
			&VarDecl{NameList: []*Name{{Value: "x"}}, Values: &BasicLit{Value: "1", Kind: 7}},
			&VarDecl{NameList: []*Name{pkg}, Values: call},
		},
	}
}
`
	const main = `package main

import "example.com/exprast/tree"

func main() { tree.Fdump(tree.Sample()) }
`
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "tree"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, source := range map[string]string{
		"go.mod":       "module example.com/exprast\n\ngo 1.27\n",
		"main.go":      main,
		"tree/tree.go": dependency,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	differGoSourceModule(t, dir)
}

// TestS809ReflectMaterialisedPromotedMethod is the tightest witness of the
// same boundary: reflect.Value.Interface() hands the original program back a
// pointer to a materialised original type, and the program then calls the
// method it declared on the embedded struct. The helper mirrors no such
// method, so before the origin was recovered the call parked on a helper that
// could not answer it.
func TestS809ReflectMaterialisedPromotedMethod(t *testing.T) {
	const dependency = `package tree

import (
	"fmt"
	"reflect"
)

type Pos uint32

type node struct{ pos Pos }

func (n *node) Pos() Pos { return n.pos }

type Name struct {
	Value string
	node
}

func Dump(n *Name) {
	x := reflect.ValueOf(n)
	if y, ok := x.Interface().(*Name); ok {
		fmt.Printf("%s @ %v\n", y.Value, y.Pos())
	}
}

func Sample() *Name { return &Name{Value: "main"} }
`
	const main = `package main

import "example.com/promoted/tree"

func main() { tree.Dump(tree.Sample()) }
`
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "tree"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, source := range map[string]string{
		"go.mod":       "module example.com/promoted\n\ngo 1.27\n",
		"main.go":      main,
		"tree/tree.go": dependency,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	differGoSourceModule(t, dir)
}
