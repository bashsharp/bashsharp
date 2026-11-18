//go:build full

package interp_test

// Sprint: #281; Story: #809; Story-ID: fac7e14af4a8

import (
	"os"
	"path/filepath"
	"testing"
)

// A linked interpreted package owns this cyclic AST and its reflect walker.
// The native oracle and interpreter must observe the same concrete types and
// pointer identity, including the Owner edge back to the already dumped File.
func TestS281ReflectWalkInterpretedAST(t *testing.T) {
	const dependency = `package tree

import (
	"fmt"
	"reflect"
)

type Node interface{ node() }

type Decl interface {
	Node
	decl()
}

type File struct {
	Name  *Name
	Decls []Decl
}

func (*File) node() {}

type Name struct {
	Value string
	Owner *File
}

func (*Name) node() {}

type GenDecl struct {
	Name  *Name
	Owner *File
}

func (*GenDecl) node() {}
func (*GenDecl) decl() {}

type dumper struct {
	ptrmap map[Node]int
	next   int
}

func (p *dumper) dump(x reflect.Value) {
	switch x.Kind() {
	case reflect.Interface:
		if x.IsNil() {
			fmt.Print("nil")
			return
		}
		p.dump(x.Elem())
	case reflect.Ptr:
		if x.IsNil() {
			fmt.Print("nil")
			return
		}
		if n, ok := x.Interface().(Node); ok {
			if id, exists := p.ptrmap[n]; exists {
				fmt.Printf("ref(%d)", id)
				return
			}
			p.next++
			p.ptrmap[n] = p.next
			fmt.Printf("#%d*", p.next)
		} else {
			fmt.Print("*")
		}
		p.dump(x.Elem())
	case reflect.Slice:
		fmt.Printf("%s[%d]{", x.Type(), x.Len())
		for i := 0; i < x.Len(); i++ {
			if i > 0 {
				fmt.Print(",")
			}
			p.dump(x.Index(i))
		}
		fmt.Print("}")
	case reflect.Struct:
		typ := x.Type()
		fmt.Printf("%s{", typ)
		for i := 0; i < x.NumField(); i++ {
			if i > 0 {
				fmt.Print(",")
			}
			fmt.Printf("%s:", typ.Field(i).Name)
			p.dump(x.Field(i))
		}
		fmt.Print("}")
	default:
		fmt.Printf("%v", x.Interface())
	}
}

func Dump(n Node) {
	p := dumper{ptrmap: make(map[Node]int)}
	p.dump(reflect.ValueOf(n))
	fmt.Println()
	fmt.Printf("%T\n", reflect.TypeOf(n))
}

func Sample() *File {
	f := &File{}
	n := &Name{Value: "root", Owner: f}
	f.Name = n
	f.Decls = []Decl{&GenDecl{Name: n, Owner: f}}
	return f
}
`
	const main = `package main

import "example.com/reflectast/dep"

func main() { tree.Dump(tree.Sample()) }
`
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "dep"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, source := range map[string]string{
		"go.mod":      "module example.com/reflectast\n\ngo 1.27\n",
		"main.go":     main,
		"dep/tree.go": dependency,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	differGoSourceModule(t, dir)
}
