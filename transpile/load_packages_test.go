// Sprint: #149; Story: S149.12; Story-ID: e9c799a66ea7
package transpile

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/bashsharp/bashsharp/front"
)

// TestGoSourcePackageMapEndToEnd loads a two-package set with a relative
// import through the linked front end and checks the recorded resolutions
// are exactly the map decisions, in order, and identical across runs.
func TestGoSourcePackageMapEndToEnd(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	a := write("a.go", "package a\n\nfunc F() int { return 1 }\n")
	b := write("b.go", "package b\n\nimport \"./a\"\n\nfunc G() int { return a.F() + 1 }\n")
	c := write("c.go", "package main\n\nimport (\n\t\"fmt\"\n\t\"./b\"\n)\n\nfunc main() { fmt.Println(b.G()) }\n")
	read := func(path string) front.GoSourceFile {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return front.GoSourceFile{Name: path, Data: data}
	}
	opts := front.GoSourceOptions{
		Dir:        dir,
		ImportBase: "test",
		Packages: []front.GoSourcePackage{
			{Path: "test/a", Files: []front.GoSourceFile{read(a)}},
			{Path: "test/b", Files: []front.GoSourceFile{read(b)}},
		},
	}
	want := []front.GoSourceImportResolution{
		{From: "test/b", Import: "./a", Path: "test/a", Origin: "package-map", Name: "a", Files: []string{a}},
		{From: "main", Import: "fmt", Path: "fmt", Origin: "importer", Name: "fmt"},
		{From: "main", Import: "./b", Path: "test/b", Origin: "package-map", Name: "b", Files: []string{b}},
	}
	for run := 0; run < 3; run++ {
		prog, err := loadGoSource([]front.GoSourceFile{read(c)}, opts)
		if err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		if !reflect.DeepEqual(prog.Resolutions, want) {
			t.Fatalf("run %d: resolutions =\n%#v\nwant\n%#v", run, prog.Resolutions, want)
		}
	}
	// Without the base the relative import is refused, never looked up on disk.
	if _, err := loadGoSource([]front.GoSourceFile{read(b)}, front.GoSourceOptions{Dir: dir, Packages: opts.Packages[:1]}); err == nil {
		t.Fatal("relative import without --go-import-base must be refused")
	}
}
