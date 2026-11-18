//go:build full

package interp

// Sprint: #281; Story: #809; Story-ID: fac7e14af4a8

import (
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
)

func TestS281ReflectASTTypeDescriptors(t *testing.T) {
	main := gosource.Source{Name: "main.go", Data: []byte(`package main
import "test/tree"
func main() { _ = tree.Sample() }
`)}
	dependency := gosource.Source{Name: "tree.go", Data: []byte(`package tree
type Node interface { node() }
type Decl interface { Node; decl() }
type File struct { Decls []Decl }
func (*File) node() {}
type GenDecl struct { Owner *File }
func (*GenDecl) node() {}
func (*GenDecl) decl() {}
type Number interface { ~int | ~int64 }
type Constraint interface { Number }
func Sample() *File { f := &File{}; f.Decls = []Decl{&GenDecl{Owner:f}}; return f }
`)}
	program, err := gosource.Load([]gosource.Source{main}, gosource.Options{
		RunMain:  true,
		Packages: []gosource.PackageSpec{{Path: "test/tree", Sources: []gosource.Source{dependency}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := &Runner{bashPPGoSource: true, bashPPGoSourceFile: program.File}
	descriptors := make(map[string]bashPPLocalType)
	for _, descriptor := range runner.bashPPLocalTypeDescriptors() {
		descriptors[descriptor.Name] = descriptor
	}
	for _, name := range []string{"__gosource_pkg_0_Node", "__gosource_pkg_0_Decl", "__gosource_pkg_0_File", "__gosource_pkg_0_GenDecl"} {
		if _, ok := descriptors[name]; !ok {
			t.Fatalf("reachable interpreted type %s was not materialised: %+v", name, descriptors)
		}
	}
	if decl := descriptors["__gosource_pkg_0_Decl"]; !decl.refs["__gosource_pkg_0_Node"] {
		t.Fatalf("embedded interface edge was not registered: %+v", decl)
	}
	if file := descriptors["__gosource_pkg_0_File"]; !file.refs["__gosource_pkg_0_Decl"] {
		t.Fatalf("slice/interface edge was not registered: %+v", file)
	}
	if concrete := descriptors["__gosource_pkg_0_GenDecl"]; !concrete.refs["__gosource_pkg_0_File"] {
		t.Fatalf("pointer cycle edge was not registered: %+v", concrete)
	}
	for _, name := range []string{"__gosource_pkg_0_Number", "__gosource_pkg_0_Constraint"} {
		if _, ok := descriptors[name]; ok {
			t.Fatalf("constraint-only interface %s was materialised", name)
		}
	}
}
