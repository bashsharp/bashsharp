package gosource_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/lower"
)

// A package map with no declared ImportPath, checked through the module
// importer: completing fmt's incomplete transitive dependencies (internal/poll,
// …) to look for a mapped package must not apply the program's internal
// visibility rule to them. Regression from 2a7c0089 (Sprint 243), caught by
// bashsharp's TestGoSourcePackageMapEndToEnd.
func TestS269MapCompletesStdlibTransitiveImports(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) gosource.Source {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return gosource.Source{Name: path, Data: []byte(body)}
	}
	a := write("a.go", "package a\n\nfunc F() int { return 1 }\n")
	b := write("b.go", "package b\n\nimport \"./a\"\n\nfunc G() int { return a.F() + 1 }\n")
	c := write("c.go", "package main\n\nimport (\n\t\"fmt\"\n\t\"./b\"\n)\n\nfunc main() { fmt.Println(b.G()) }\n")
	_, err := gosource.Load([]gosource.Source{c}, gosource.Options{
		Importer:   lower.NewModuleImporter(dir),
		ImportBase: "test",
		Packages: []gosource.PackageSpec{
			{Path: "test/a", Sources: []gosource.Source{a}},
			{Path: "test/b", Sources: []gosource.Source{b}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
}
