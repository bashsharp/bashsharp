package gosource_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/lower"
)

// Sprint 171 W1: an explicit package map lowered one native unit per package.
//
// The interpreter flattens a package map into one file and renames every
// mapped package-level name under the hygiene prefix. A compiled unit must
// not: gc's -m notes, recursive-type and identity diagnostics, reflect's
// PkgPath and the linker's init scheduling all read the original names and
// package clauses. With PreserveNativeInit the map only serves the checker,
// and each package lowers to a unit of itself with its imports kept.

// unitPackage is one package of a fixture, in dependency order.
type unitPackage struct {
	dir   string // module-relative directory and import path suffix
	files []string
}

// lowerUnits loads and lowers every package of a fixture as its own native
// unit, each checked against the packages before it, exactly as cmd/go
// compiles them. It returns the generated files by package directory.
func lowerUnits(t *testing.T, fixture, module string, packages []unitPackage) map[string]*lower.Result {
	t.Helper()
	dir := filepath.Join("testdata", "sprint171", "w1-units", fixture)
	var specs []gosource.PackageSpec
	results := map[string]*lower.Result{}
	for _, pkg := range packages {
		var sources []gosource.Source
		for _, name := range pkg.files {
			data, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			sources = append(sources, gosource.Source{Name: name, Data: data})
		}
		path := module + "/" + pkg.dir
		program, err := gosource.Load(sources, gosource.Options{PreserveNativeInit: true, ImportPath: path, Packages: specs})
		if err != nil {
			t.Fatalf("%s: Load %s: %v", fixture, path, err)
		}
		if len(program.Packages) != 0 {
			t.Fatalf("%s: native unit %s linked %d mapped packages", fixture, path, len(program.Packages))
		}
		result, err := lower.Compile(program.File, lower.Options{Package: program.Package, Library: true, Importer: program.Importer})
		if err != nil {
			t.Fatalf("%s: lower %s: %v", fixture, path, err)
		}
		if len(result.Files) != len(pkg.files) {
			t.Fatalf("%s: %s emitted %d files, want %d", fixture, path, len(result.Files), len(pkg.files))
		}
		for _, output := range result.Files {
			text := string(output.Source)
			if strings.Contains(text, "__gosource") {
				t.Errorf("%s: %s/%s carries a hygiene-prefixed name:\n%s", fixture, pkg.dir, output.Name, text)
			}
			if !strings.Contains(text, "\npackage "+program.Package+"\n") {
				t.Errorf("%s: %s/%s lost its package clause:\n%s", fixture, pkg.dir, output.Name, text)
			}
		}
		results[pkg.dir] = result
		specs = append(specs, gosource.PackageSpec{Path: path, Sources: sources})
	}
	return results
}

// writeModule materialises one file set per package directory as a module
// so the Go toolchain, not this test, judges it.
func writeModule(t *testing.T, module string, files map[string]map[string][]byte) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module "+module+"\n\ngo 1.23\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for dir, outputs := range files {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		for name, data := range outputs {
			if err := os.WriteFile(filepath.Join(root, dir, name), data, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	return root
}

// generatedFiles is the module layout of the lowered units.
func generatedFiles(results map[string]*lower.Result) map[string]map[string][]byte {
	files := map[string]map[string][]byte{}
	for dir, result := range results {
		files[dir] = map[string][]byte{}
		for _, output := range result.Files {
			files[dir][output.Name] = output.Source
		}
	}
	return files
}

// originalFiles is the same module layout built from the unchanged fixture,
// the oracle a generated unit's compiler notes are compared against.
func originalFiles(t *testing.T, fixture string, packages []unitPackage) map[string]map[string][]byte {
	t.Helper()
	dir := filepath.Join("testdata", "sprint171", "w1-units", fixture)
	files := map[string]map[string][]byte{}
	for _, pkg := range packages {
		files[pkg.dir] = map[string][]byte{}
		for _, name := range pkg.files {
			data, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			files[pkg.dir][name] = data
		}
	}
	return files
}

// compilerNotes returns gc's -m notes for a module as file:line: message,
// sorted, without the per-package headers cmd/go interleaves. The column is
// dropped: errorcheck matches notes by file and line, and a statement's
// //line directive re-bases the column at the generated indentation, which
// is the emitter's positioning, not the names under test here.
var notePosition = regexp.MustCompile(`^([^:]+:\d+):\d+: `)

func compilerNotes(t *testing.T, root string) []string {
	t.Helper()
	output, err := goCommand(t, root, "build", "-gcflags=-m", "./...")
	if err != nil {
		t.Fatalf("go build -gcflags=-m: %v\n%s", err, output)
	}
	var notes []string
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if !strings.HasPrefix(line, "# ") {
			notes = append(notes, notePosition.ReplaceAllString(line, "$1: "))
		}
	}
	sort.Strings(notes)
	return notes
}

func goCommand(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod")
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func unitSource(t *testing.T, results map[string]*lower.Result, dir, name string) string {
	t.Helper()
	for _, output := range results[dir].Files {
		if output.Name == name {
			return string(output.Source)
		}
	}
	t.Fatalf("no generated %s/%s", dir, name)
	return ""
}

// TestNativeUnitsKeepPackageNames: gc's -m notes on the generated units name
// the original functions and methods, and the importing unit spells its
// dependency as an import.
func TestNativeUnitsKeepPackageNames(t *testing.T) {
	const module = "example.com/units"
	packages := []unitPackage{{"p", []string{"p.go"}}, {"q", []string{"q.go"}}}
	results := lowerUnits(t, "two-package-names", module, packages)
	q := unitSource(t, results, "q", "q.go")
	for _, want := range []string{"\npackage q\n", "import \"example.com/units/p\"\n", "p.F()"} {
		if !strings.Contains(q, want) {
			t.Errorf("q.go lacks %q:\n%s", want, q)
		}
	}
	// The oracle is gc on the unchanged fixture: every note it reports on
	// the original names and positions, the generated units must report too.
	want := compilerNotes(t, writeModule(t, module, originalFiles(t, "two-package-names", packages)))
	got := compilerNotes(t, writeModule(t, module, generatedFiles(results)))
	if !reflect.DeepEqual(got, want) {
		t.Errorf("-m notes differ\n--- original\n%s\n--- generated\n%s", strings.Join(want, "\n"), strings.Join(got, "\n"))
	}
	for _, note := range []string{"p/p.go:3: can inline F", "p/p.go:14: can inline t.m", "q/q.go:6: inlining call to p.F"} {
		if !slices.Contains(want, note) {
			t.Errorf("oracle lacks %q; the fixture no longer exercises -m name fidelity:\n%s", note, strings.Join(want, "\n"))
		}
	}
}

// TestNativeUnitsKeepRelativeImports: the compiler's -D form of a mapped
// import is kept as written, so gc resolves it through the same -D and
// -importcfg the original package was compiled with.
func TestNativeUnitsKeepRelativeImports(t *testing.T) {
	dir := filepath.Join("testdata", "sprint171", "w1-units", "two-package-names")
	p, err := os.ReadFile(filepath.Join(dir, "p.go"))
	if err != nil {
		t.Fatal(err)
	}
	q := "package q\n\nimport \"./p\"\n\nfunc H() { p.F() }\n"
	program, err := gosource.Load([]gosource.Source{{Name: "q.go", Data: []byte(q)}}, gosource.Options{
		PreserveNativeInit: true, ImportBase: "test", ImportPath: "test/q",
		Packages: []gosource.PackageSpec{{Path: "test/p", Sources: []gosource.Source{{Name: "p.go", Data: p}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := lower.Compile(program.File, lower.Options{Package: program.Package, Library: true, Importer: program.Importer})
	if err != nil {
		t.Fatal(err)
	}
	text := string(result.Files[0].Source)
	for _, want := range []string{"\npackage q\n", "import \"./p\"\n", "p.F()"} {
		if !strings.Contains(text, want) {
			t.Errorf("q.go lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "__gosource") {
		t.Errorf("q.go carries a hygiene-prefixed name:\n%s", text)
	}
}

// TestNativeUnitsKeepCrossPackageTypes: an embedded type from a mapped
// package, a method set split across packages and a variable named like a
// mapped type all keep their identity, so the units build and run as the
// original program does.
func TestNativeUnitsKeepCrossPackageTypes(t *testing.T) {
	const module = "example.com/rec"
	results := lowerUnits(t, "recursive-type-across-packages", module, []unitPackage{{"a", []string{"a.go"}}, {"b", []string{"b.go"}}, {"main", []string{"main.go"}}})
	b := unitSource(t, results, "b", "b.go")
	for _, want := range []string{"type T struct{ a.T }", "\ta.I\n", "\ta.V\n", "var V struct{ i int }"} {
		if !strings.Contains(b, want) {
			t.Errorf("b.go lacks %q:\n%s", want, b)
		}
	}
	root := writeModule(t, module, generatedFiles(results))
	output, err := goCommand(t, root, "run", "./main")
	if err != nil {
		t.Fatalf("go run: %v\n%s", err, output)
	}
	if output != "ok\nok\n" {
		t.Errorf("program output = %q, want two ok lines", output)
	}
}

// TestNativeUnitsKeepInit: a mapped package's init stays a native init the
// linker schedules under its own package, and the program's init stays init.
func TestNativeUnitsKeepInit(t *testing.T) {
	const module = "example.com/init"
	results := lowerUnits(t, "package-init", module, []unitPackage{{"a", []string{"a.go"}}, {"main", []string{"main.go"}}})
	for _, unit := range []struct{ dir, name string }{{"a", "a.go"}, {"main", "main.go"}} {
		text := unitSource(t, results, unit.dir, unit.name)
		if !strings.Contains(text, "\nfunc init() {") || strings.Contains(text, "init_") {
			t.Errorf("%s/%s does not keep its native init:\n%s", unit.dir, unit.name, text)
		}
	}
	if main := unitSource(t, results, "main", "main.go"); !strings.Contains(main, "import (\n\t\"example.com/init/a\"\n)") && !strings.Contains(main, "import \"example.com/init/a\"") {
		t.Errorf("main.go lacks the import of a:\n%s", main)
	}
	root := writeModule(t, module, generatedFiles(results))
	output, err := goCommand(t, root, "run", "./main")
	if err != nil {
		t.Fatalf("go run: %v\n%s", err, output)
	}
	if output != "a.init 1\nmain.init 1\nmain 1\n" {
		t.Errorf("program output = %q", output)
	}
}

// TestNativeUnitsRefuseExecution: a native unit has no synthetic entry, so
// RunMain is refused as before; and without PreserveNativeInit the map is
// still flattened for the interpreter, names renamed and nothing imported.
func TestNativeUnitsRefuseExecution(t *testing.T) {
	dir := filepath.Join("testdata", "sprint171", "w1-units", "two-package-names")
	read := func(name string) gosource.Source {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return gosource.Source{Name: name, Data: data}
	}
	packages := []gosource.PackageSpec{{Path: "example.com/units/p", Sources: []gosource.Source{read("p.go")}}}
	_, err := gosource.Load([]gosource.Source{read("q.go")}, gosource.Options{PreserveNativeInit: true, RunMain: true, ImportPath: "example.com/units/q", Packages: packages})
	if err == nil || !strings.Contains(err.Error(), "non-executing package") {
		t.Fatalf("RunMain with native units: err = %v", err)
	}
	program, err := gosource.Load([]gosource.Source{read("q.go")}, gosource.Options{ImportPath: "example.com/units/q", Packages: packages})
	if err != nil {
		t.Fatal(err)
	}
	if len(program.Packages) != 1 || program.Packages[0].Path != "example.com/units/p" {
		t.Fatalf("flattened program links %v", program.Packages)
	}
	result, err := lower.Compile(program.File, lower.Options{Package: program.Package, Importer: program.Importer})
	if err != nil {
		t.Fatal(err)
	}
	text := string(result.Source)
	if !strings.Contains(text, "__gosource_pkg_0_F") || strings.Contains(text, "import \"example.com/units/p\"") {
		t.Errorf("flattened unit changed shape:\n%s", text)
	}
}

// TestNativeUnitsCompileDirectly is the compiler route of a directory test:
// each package's flat unit is handed to `go tool compile -D test -p test/<pkg>`
// with an importcfg naming the earlier packages' objects, so the mapped import
// is resolved by gc from the object and its -m notes name the original
// functions.
func TestNativeUnitsCompileDirectly(t *testing.T) {
	dir := filepath.Join("testdata", "sprint171", "w1-units", "two-package-names")
	p, err := os.ReadFile(filepath.Join(dir, "p.go"))
	if err != nil {
		t.Fatal(err)
	}
	q := "package q\n\nimport \"./p\"\n\nfunc H() {\n\tp.F()\n\tprint(1, 2, 3, 4, 5, 6, 7, 8, 9, 10)\n}\n"
	work := t.TempDir()
	stdlib, err := goCommand(t, work, "list", "-export", "-f", "{{if .Export}}packagefile {{.ImportPath}}={{.Export}}{{end}}", "std")
	if err != nil {
		t.Fatalf("go list -export std: %v\n%s", err, stdlib)
	}
	importcfg := filepath.Join(work, "importcfg")
	if err := os.WriteFile(importcfg, []byte(stdlib+"\npackagefile test/p="+filepath.Join(work, "p.a")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	flat := func(name string, data []byte, path string, packages []gosource.PackageSpec) string {
		t.Helper()
		program, err := gosource.Load([]gosource.Source{{Name: name, Data: data}}, gosource.Options{PreserveNativeInit: true, ImportBase: "test", ImportPath: path, Packages: packages})
		if err != nil {
			t.Fatal(err)
		}
		result, err := lower.Compile(program.File, lower.Options{Package: program.Package, Importer: program.Importer})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(result.Source), "__gosource") {
			t.Errorf("%s carries a hygiene-prefixed name:\n%s", name, result.Source)
		}
		generated := filepath.Join(work, "gen_"+name)
		if err := os.WriteFile(generated, result.Source, 0o644); err != nil {
			t.Fatal(err)
		}
		return generated
	}
	generatedP := flat("p.go", p, "test/p", nil)
	output, err := goCommand(t, work, "tool", "compile", "-e", "-D", "test", "-importcfg="+importcfg, "-o", filepath.Join(work, "p.a"), "-p", "test/p", "-m", generatedP)
	if err != nil {
		t.Fatalf("compile p: %v\n%s", err, output)
	}
	for _, want := range []string{"p.go:3:6: can inline F\n", "p.go:8:4: inlining call to F\n", "p.go:14:6: can inline t.m\n"} {
		if !strings.Contains(output, want) {
			t.Errorf("p -m notes lack %q:\n%s", want, output)
		}
	}
	generatedQ := flat("q.go", []byte(q), "test/q", []gosource.PackageSpec{{Path: "test/p", Sources: []gosource.Source{{Name: "p.go", Data: p}}}})
	output, err = goCommand(t, work, "tool", "compile", "-e", "-D", "test", "-importcfg="+importcfg, "-o", filepath.Join(work, "q.a"), "-p", "test/q", "-m", generatedQ)
	if err != nil {
		t.Fatalf("compile q: %v\n%s", err, output)
	}
	if !strings.Contains(output, "q.go:6:6: inlining call to p.F\n") || strings.Contains(output, "__gosource") || strings.Contains(output, "can inline F") {
		t.Errorf("q -m notes are not the original package's alone:\n%s", output)
	}
}

// stdlibImportcfg writes the standard library's importcfg for a direct
// `go tool compile` under work.
func stdlibImportcfg(t *testing.T, work string) string {
	t.Helper()
	stdlib, err := goCommand(t, work, "list", "-export", "-f", "{{if .Export}}packagefile {{.ImportPath}}={{.Export}}{{end}}", "std")
	if err != nil {
		t.Fatalf("go list -export std: %v\n%s", err, stdlib)
	}
	importcfg := filepath.Join(work, "importcfg")
	if err := os.WriteFile(importcfg, []byte(stdlib), 0o644); err != nil {
		t.Fatal(err)
	}
	return importcfg
}

// TestFloatingDirectivesReachCompiler: a //go:linkname written after the
// last declaration or above a type documents no declaration, yet gc reads
// it wherever it stands. The lowered unit carries every such directive at
// its own source position, so gc accepts the well-formed one and refuses an
// undeclared target and a duplicate on the lines the source wrote them.
func TestFloatingDirectivesReachCompiler(t *testing.T) {
	dir := filepath.Join("testdata", "sprint171", "w1-units", "floating-pragma")
	work := t.TempDir()
	importcfg := stdlibImportcfg(t, work)
	compile := func(name string) (string, error) {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		program, err := gosource.Load([]gosource.Source{{Name: name, Data: data}}, gosource.Options{PreserveNativeInit: true, ImportPath: "p"})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		result, err := lower.Compile(program.File, lower.Options{Package: program.Package, Importer: program.Importer})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := strings.Count(string(result.Source), "//go:linkname"); got != strings.Count(string(data), "//go:linkname") {
			t.Errorf("%s: %d linkname directives lowered, want every one:\n%s", name, got, result.Source)
		}
		generated := filepath.Join(work, name)
		if err := os.WriteFile(generated, result.Source, 0o644); err != nil {
			t.Fatal(err)
		}
		return goCommand(t, work, "tool", "compile", "-e", "-p=p", "-importcfg="+importcfg, "-o", filepath.Join(work, name+".o"), generated)
	}
	if output, err := compile("positive.go"); err != nil {
		t.Errorf("positive.go: %v\n%s", err, output)
	}
	output, err := compile("reject.go")
	if err == nil {
		t.Fatalf("reject.go compiled; gc never saw its directives:\n%s", output)
	}
	var got []string
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		got = append(got, strings.TrimPrefix(line, work+string(filepath.Separator)))
	}
	// gc's own positions on the unchanged file: the directive text after
	// the comment marker, on the line the source wrote it.
	want := []string{
		"reject.go:5:3: //go:linkname must refer to declared function or variable",
		"reject.go:13:3: duplicate //go:linkname for x",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("gc diagnostics\ngot:  %q\nwant: %q", got, want)
	}
}
