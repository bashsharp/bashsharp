package gosource

import (
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Sprint: #270; Story: #761; Story-ID: c8365c7b8c50
//
// A linked package's //go:nointerface travels with its method (the
// interpreter and gc both read it from the flattened unit), while that
// package's other declaration directives keep their existing treatment
// (fixedbugs/issue30862.go).
func TestLinkedPackageNointerfaceDirective(t *testing.T) {
	a := PackageSpec{Path: "example.com/m/a", Sources: []Source{src("a.go", `package a

type S struct{ F int }

//go:nointerface
func (s *S) Hidden() {}

func (s *S) Shown() {}

//go:noinline
func Helper() int { return 1 }
`)}}
	prog, err := Load([]Source{src("main.go", `package main

import "example.com/m/a"

func main() { _ = a.Helper(); (&a.S{}).Hidden() }
`)}, Options{ImportPath: "example.com/m", Packages: []PackageSpec{a}, RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	directives := map[string][]string{}
	for _, stmt := range prog.File.Stmts {
		fn, ok := stmt.Cmd.(*syntax.BashPPFuncDecl)
		if !ok {
			continue
		}
		for _, c := range stmt.Comments {
			directives[fn.Name.Value] = append(directives[fn.Name.Value], c.Text)
		}
	}
	var hidden, shown, helper []string
	for name, list := range directives {
		switch {
		case name == "Hidden":
			hidden = list
		case name == "Shown":
			shown = list
		case len(name) >= 6 && name[len(name)-6:] == "Helper":
			helper = list
		}
	}
	if len(hidden) != 1 || hidden[0] != "go:nointerface" {
		t.Fatalf("Hidden directives = %q (all %q)", hidden, directives)
	}
	if len(shown) != 0 || len(helper) != 0 {
		t.Fatalf("unexpected linked directives: Shown %q, Helper %q", shown, helper)
	}
}
