package gosource_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/lower"
)

// TestExternalTestUnitSeesPtestAndSpellsBare pins the mechanism behind the
// go/types, types2 and cmd/compile/internal/types package rows of Sprint
// 171: cmd/go checks an external test package (x_test) against ptest — the
// tested package WITH its in-package test files — so (1) an identifier one
// of those files exports for the external tests resolves, (2) the external
// unit is checked under its own path <pkg>_test while its visibility
// identity stays <pkg>, so the tested package's types it uses keep their
// qualifier, and (3) a type spelled by the converter in a file that
// dot-imports the tested package stays bare (an instantiation's arguments
// here). The tested unit is handed to the external one as an explicit
// package under the tested identity, which is how the transpile does it.
func TestExternalTestUnitSeesPtestAndSpellsBare(t *testing.T) {
	dir := filepath.Join("testdata", "sprint171", "manager", "xtest-ptest")
	read := func(name string) gosource.Source {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return gosource.Source{Name: name, Data: data}
	}
	const pkg = "example.com/xtp/lib"
	tested := []gosource.Source{read("lib.go"), read("export_test.go")}
	xtest := []gosource.Source{read("x_test.go"), read("y_test.go")}

	prog, err := gosource.Load(xtest, gosource.Options{PreserveNativeInit: true, ImportPath: pkg, Packages: []gosource.PackageSpec{{Path: pkg, Sources: tested}}})
	if err != nil {
		t.Fatalf("external test unit against ptest: %v", err)
	}
	result, err := lower.Compile(prog.File, lower.Options{Package: prog.Package, Library: true, Importer: prog.Importer})
	if err != nil {
		t.Fatalf("external test unit did not lower: %v", err)
	}
	generated := map[string]string{}
	for _, f := range result.Files {
		generated[filepath.Base(f.Name)] = string(f.Source)
	}
	dot, named := generated["x_test.go"], generated["y_test.go"]
	if !strings.Contains(dot, "package lib_test") || !strings.Contains(dot, `. "example.com/xtp/lib"`) {
		t.Fatalf("external unit lost its identity or dot import:\n%s", dot)
	}
	if strings.Contains(dot, "lib.Sym") || strings.Contains(dot, "lib_test.") {
		t.Fatalf("a dot-imported or own type was qualified:\n%s", dot)
	}
	if !strings.Contains(named, "lib.Sym") || strings.Contains(named, "lib_test.") {
		t.Fatalf("a named import's type lost its qualifier (the cmd/compile/internal/types row):\n%s", named)
	}

	// Without ptest (the plain package only) the exported test helper is
	// unresolved — the go/types api_test.go shape.
	if _, err := gosource.Load(xtest, gosource.Options{PreserveNativeInit: true, ImportPath: pkg, Packages: []gosource.PackageSpec{{Path: pkg, Sources: tested[:1]}}}); err == nil || !strings.Contains(err.Error(), "CmpN") {
		t.Fatalf("plain package must not export the test helper: %v", err)
	}
}
