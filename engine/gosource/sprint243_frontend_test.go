package gosource_test

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestS243RecoveredGoDeferUsesExactDeclarations(t *testing.T) {
	src := `package p
import (
	"fmt"
	"math"
)
func f() {
	i := 1
	defer fmt.Sprint
	go math.Sin
	go i
	var unused int
}
`
	want := []string{
		"recover.go:8:18: expression in defer must be function call",
		"recover.go:9:13: expression in go must be function call",
		"recover.go:10:6: expression in go must be function call",
		"recover.go:11:6: declared and not used: unused",
	}
	got := sprint165Diagnostics(t, "recover.go", []byte(src), gosource.Options{})
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got: %q\nwant: %q", got, want)
	}
}

func TestS243RecoveredSelectorPreservesUnusedDeclarations(t *testing.T) {
	tests := []struct {
		name, body string
		unused     []string
	}{
		{"selector field", "x := struct{ y int }{}; y := 1; defer x.y", []string{"y := 1"}},
		{"nested selector", "x := struct{ y struct{ z int } }{}; y := 1; z := 2; defer x.y.z", []string{"y := 1", "z := 2"}},
		{"shadowed outer", "y := 1; { y := 2; defer y }", []string{"y := 1"}},
		{"independent declaration", "{ y := 1; defer y\n }; { y := 2 }", []string{"y := 2"}},
		{"lexical selector base", "x := struct{ y int }{}; defer x.y", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sprint165Diagnostics(t, "recover.go", []byte("package p\nfunc f() { "+tt.body+" }\n"), gosource.Options{})
			var unused []string
			for _, diagnostic := range got {
				if strings.Contains(diagnostic, "declared and not used: ") {
					unused = append(unused, diagnostic)
				}
			}
			var want []string
			for _, decl := range tt.unused {
				name := strings.Fields(decl)[0]
				prefix := "func f() { " + tt.body[:strings.Index(tt.body, decl)]
				line := 2 + strings.Count(prefix, "\n")
				col := len(prefix) - strings.LastIndex(prefix, "\n")
				want = append(want, fmt.Sprintf("recover.go:%d:%d: declared and not used: %s", line, col, name))
			}
			if !reflect.DeepEqual(unused, want) {
				t.Fatalf("unused = %q, want %q; diagnostics: %q", unused, want, got)
			}
		})
	}
}

// TestS243EmbeddedTypeKeepsPackageIdentity covers the interpreted, flattened
// package shape. The embedded field belongs to package b, but its type object
// belongs to package a; its lowered name must follow the type object rather
// than an identically named declaration in b.
func TestS243EmbeddedTypeKeepsPackageIdentity(t *testing.T) {
	a := gosource.PackageSpec{Path: "example.com/p/a", Sources: []gosource.Source{{
		Name: "a.go", Data: []byte("package a\n\ntype T int\n"),
	}}}
	b := gosource.PackageSpec{Path: "example.com/p/b", Sources: []gosource.Source{{
		Name: "b.go", Data: []byte("package b\n\nimport \"example.com/p/a\"\n\ntype T struct { a.T }\n"),
	}}}
	main := gosource.Source{Name: "main.go", Data: []byte("package main\n\nimport \"example.com/p/b\"\n\nvar _ = b.T{}\n")}
	program, err := gosource.Load([]gosource.Source{main}, gosource.Options{Packages: []gosource.PackageSpec{a, b}})
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := syntax.NewPrinter().Print(&out, program.File); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if !strings.Contains(text, "type __gosource_pkg_1_T struct { __gosource_pkg_0_T }") {
		t.Fatalf("embedded type lost package identity:\n%s", text)
	}
}
