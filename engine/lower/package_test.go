package lower_test

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/lower"
)

// Sprint 162 S162.4 wave 2: a Go package that is not main lowers to that
// package, with no synthesised main. gc compiles `package escape` as it is,
// and a made-up `func main() {}` is one more function whose "can inline
// main" note upstream errorcheck reports as unmatched on a -m root. The
// caller names the package (Options.Package, the front end's
// Program.Package); the reproducer under testdata/sprint162/package is
// outside the corpus. The negative: the same input lowered as package main
// (the caller's default) still gets its main, since a main package without
// one does not build.
func TestGoSourceNonMainPackage(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "sprint162", "package", "escape.go"))
	if err != nil {
		t.Fatal(err)
	}
	program, err := gosource.Parse(bytes.NewReader(data), "escape.go", gosource.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if program.Package != "escape" {
		t.Fatalf("front end reports package %q", program.Package)
	}
	for _, pkg := range []string{"escape", ""} {
		result, err := lower.Compile(program.File, lower.Options{Origin: "escape.go", Package: pkg})
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(token.NewFileSet(), "escape.generated.go", result.Source, 0)
		if err != nil {
			t.Fatalf("go/parser rejects the generated Go: %v\n%s", err, result.Source)
		}
		var funcs []string
		for _, d := range file.Decls {
			if fn, ok := d.(*ast.FuncDecl); ok {
				funcs = append(funcs, fn.Name.Name)
			}
		}
		want := "M F"
		if pkg == "" {
			pkg, want = "main", "M F main"
		}
		if file.Name.Name != pkg || strings.Join(funcs, " ") != want {
			t.Errorf("package %q with funcs %q; want %q with %q\n%s", file.Name.Name, funcs, pkg, want, result.Source)
		}
		if in, out := fidelityNormalize(t, data), fidelityNormalize(t, result.Source); pkg == "escape" && in != out {
			t.Errorf("generated Go is not the input\n--- want\n%s\n--- got\n%s", in, out)
		}
	}
}
