//go:build full

package gosource

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/lower"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

const pkgA = "package a\n\nfunc F() int { return 1 }\n"
const pkgB = "package b\n\nimport \"./a\"\n\nfunc G() int { return a.F() + 1 }\n"
const mainC = "package main\n\nimport (\n\t\"fmt\"\n\t\"./b\"\n)\n\nfunc main() { fmt.Println(b.G()) }\n"

func TestPackageMapResolvesRelativeImportsThroughBase(t *testing.T) {
	prog, err := Load([]Source{src("c.go", mainC)}, Options{
		ImportBase: "test",
		Packages: []PackageSpec{
			{Path: "test/a", Sources: []Source{src("a.go", pkgA)}},
			{Path: "test/b", Sources: []Source{src("b.go", pkgB)}},
		},
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if prog.Main != "main" {
		t.Fatalf("Main = %q", prog.Main)
	}
	want := []Resolution{
		{From: "test/b", Import: "./a", Path: "test/a", Origin: "package-map", Name: "a", Files: []string{"a.go"}},
		{From: "main", Import: "fmt", Path: "fmt", Origin: "importer", Name: "fmt"},
		{From: "main", Import: "./b", Path: "test/b", Origin: "package-map", Name: "b", Files: []string{"b.go"}},
	}
	if !reflect.DeepEqual(prog.Resolutions, want) {
		t.Fatalf("Resolutions =\n%#v\nwant\n%#v", prog.Resolutions, want)
	}
}

func TestPackageMapResolutionOrderIsDeterministic(t *testing.T) {
	load := func() []Resolution {
		prog, err := Load([]Source{src("c.go", mainC)}, Options{
			ImportBase: "test",
			Packages: []PackageSpec{
				{Path: "test/a", Sources: []Source{src("a.go", pkgA)}},
				{Path: "test/b", Sources: []Source{src("b.go", pkgB)}},
			},
		})
		if err != nil {
			t.Fatalf("Load: %v", err)
		}
		return prog.Resolutions
	}
	first := load()
	for i := 0; i < 5; i++ {
		if got := load(); !reflect.DeepEqual(got, first) {
			t.Fatalf("run %d differs:\n%#v\nwant\n%#v", i, got, first)
		}
	}
}

func TestRelativeImportWithoutBaseIsRefused(t *testing.T) {
	_, err := Load([]Source{src("b.go", pkgB)}, Options{
		Packages: []PackageSpec{{Path: "test/a", Sources: []Source{src("a.go", pkgA)}}},
	})
	if err == nil || !strings.Contains(err.Error(), "relative import path requires an import base") {
		t.Fatalf("err = %v, want relative-import refusal", err)
	}
}

func TestRelativeImportEscapingBaseIsRefused(t *testing.T) {
	_, err := Load([]Source{src("b.go", "package b\n\nimport \"../a\"\n\nvar _ = a.F\n")}, Options{
		ImportBase: "test",
		Packages:   []PackageSpec{{Path: "a", Sources: []Source{src("a.go", pkgA)}}},
	})
	if err == nil || !strings.Contains(err.Error(), "escapes import base") {
		t.Fatalf("err = %v, want escape refusal", err)
	}
}

func TestRelativeImportNeverFallsThroughToDisk(t *testing.T) {
	_, err := Load([]Source{src("b.go", pkgB)}, Options{ImportBase: "test"})
	if err == nil || !strings.Contains(err.Error(), `package "test/a" is not in the explicit package map`) {
		t.Fatalf("err = %v, want map miss", err)
	}
}

func TestPackageMapTakesPrecedenceOverImporter(t *testing.T) {
	// A map entry named like a standard library package shadows it: the map
	// is consulted first, exactly as -importcfg shadows GOROOT.
	prog, err := Load([]Source{src("m.go", "package main\n\nimport \"strings\"\n\nfunc main() { _ = strings.Shadow }\n")}, Options{
		Packages: []PackageSpec{{Path: "strings", Sources: []Source{src("s.go", "package strings\n\nvar Shadow = 1\n")}}},
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(prog.Resolutions) != 1 || prog.Resolutions[0].Origin != "package-map" {
		t.Fatalf("Resolutions = %#v", prog.Resolutions)
	}
}

func TestDependencyDiagnosticsAreAttributedAndStopTheLoad(t *testing.T) {
	_, err := Load([]Source{src("c.go", mainC)}, Options{
		ImportBase: "test",
		Packages: []PackageSpec{
			{Path: "test/a", Sources: []Source{src("a.go", "package a\n\nfunc F() int { return \"x\" }\n")}},
			{Path: "test/b", Sources: []Source{src("b.go", pkgB)}},
		},
	})
	if err == nil || !strings.HasPrefix(err.Error(), "a.go:3:") {
		t.Fatalf("err = %v, want a diagnostic positioned in a.go", err)
	}
}

func TestDependencyUsesProgramCheckerConfiguration(t *testing.T) {
	_, err := Load([]Source{src("main.go", "// -lang=go1.12\npackage main\n\nimport \"./a\"\nfunc main() { _ = a.F }\n")}, Options{
		ImportBase: "test",
		Packages: []PackageSpec{{Path: "test/a", Sources: []Source{
			src("a.go", "package a\n\nvar F = 0b101\n"),
		}}},
	})
	if err == nil || !strings.Contains(err.Error(), "binary literal requires go1.13 or later") {
		t.Fatalf("dependency checker configuration: %v", err)
	}
}

func TestExplicitPackageRejectsRelativeOrDuplicatePaths(t *testing.T) {
	for _, tc := range []struct{ path, want string }{
		{"./a", "must not be relative"},
		{"", "empty import path"},
	} {
		_, err := Load([]Source{src("m.go", "package main\n\nfunc main() {}\n")}, Options{Packages: []PackageSpec{{Path: tc.path, Sources: []Source{src("a.go", pkgA)}}}})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("path %q: err = %v, want %q", tc.path, err, tc.want)
		}
	}
	_, err := Load([]Source{src("m.go", "package main\n\nfunc main() {}\n")}, Options{Packages: []PackageSpec{
		{Path: "test/a", Sources: []Source{src("a.go", pkgA)}},
		{Path: "test/a", Sources: []Source{src("a2.go", pkgA)}},
	}})
	if err == nil || !strings.Contains(err.Error(), "duplicate package path") {
		t.Errorf("duplicate: err = %v", err)
	}
}

func TestImportPathAttributesProgramResolutions(t *testing.T) {
	prog, err := Load([]Source{src("b.go", pkgB)}, Options{
		ImportBase: "test",
		ImportPath: "test/b",
		Packages:   []PackageSpec{{Path: "test/a", Sources: []Source{src("a.go", pkgA)}}},
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(prog.Resolutions) != 1 || prog.Resolutions[0].From != "test/b" {
		t.Fatalf("Resolutions = %#v", prog.Resolutions)
	}
}

// mappedProgram is one link set: the program's sources plus the explicit
// package map, in the Sprint 149 convention (import base "test", mapped
// paths "test/<name>", relative imports "./<name>").
type mappedProgram struct {
	program  []Source
	packages []PackageSpec
}

func (m mappedProgram) options(runMain bool) Options {
	return Options{RunMain: runMain, ImportBase: "test", ImportPath: "test/main", Packages: m.packages}
}

// runMapped loads the link set with RunMain and runs the lowered file through
// the interpreter exactly as a CLI would, returning stdout.
func runMapped(t *testing.T, m mappedProgram) string {
	t.Helper()
	prog, err := Load(m.program, m.options(true))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var stdout, stderr bytes.Buffer
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(t.TempDir()), interp.StdIO(nil, &stdout, &stderr))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	if err := runner.Run(ctx, prog.File); err != nil {
		t.Fatalf("Runner: %v; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	if stderr.Len() > 0 {
		t.Fatalf("Runner stderr: %q (stdout %q)", stderr.String(), stdout.String())
	}
	return stdout.String()
}

// goRunMapped runs the same link set with the Go toolchain: the sources are
// laid out as module "test" — the import base — with every mapped package at
// its path, and the relative imports the map convention uses are spelled as
// the module paths they resolve to, which module mode requires.
func goRunMapped(t *testing.T, m mappedProgram) string {
	t.Helper()
	dir := t.TempDir()
	write := func(rel string, data []byte) {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		text := regexp.MustCompile(`"\./([^"]+)"`).ReplaceAllString(string(data), `"test/$1"`)
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", []byte("module test\n\ngo 1.25\n"))
	for _, s := range m.program {
		write(s.Name, s.Data)
	}
	for _, spec := range m.packages {
		for _, s := range spec.Sources {
			write(strings.TrimPrefix(spec.Path, "test/")+"/"+s.Name, s.Data)
		}
	}
	cmd := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "run", ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod", "GOWORK=off")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("go run: %v\n%s", err, stderr.String())
	}
	return stdout.String()
}

func readFixture(t *testing.T, rel string) Source {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "sprint151", "mapped", filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return Source{Name: filepath.Base(rel), Data: data}
}

// TestMappedFixtureRunsThroughInterp runs the Sprint 151 M1 fixture end to
// end: the explicit package map is linked at lowering time, the interpreter
// never sees an import for the mapped path, and the output matches go run.
func TestMappedFixtureRunsThroughInterp(t *testing.T) {
	m := mappedProgram{
		program:  []Source{readFixture(t, "main.go")},
		packages: []PackageSpec{{Path: "test/a", Sources: []Source{readFixture(t, "a/a.go")}}},
	}
	prog, err := Load(m.program, m.options(true))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if want := []LinkedPackage{{Path: "test/a", Name: "a", Files: []string{"a.go"}}}; !reflect.DeepEqual(prog.Packages, want) {
		t.Fatalf("Packages = %#v, want %#v", prog.Packages, want)
	}
	for _, stmt := range prog.File.Stmts {
		if imp, ok := stmt.Cmd.(*syntax.BashPPImport); ok {
			if path := imp.Path.Parts[0].(*syntax.Lit).Value; path == "./a" || path == "test/a" {
				t.Fatalf("mapped import %q reached the lowered file", path)
			}
		}
	}
	if len(prog.Sources) != 2 || prog.Sources[1].Name != "a.go" {
		t.Fatalf("Sources = %#v, want main.go and a.go", prog.Sources)
	}
	want := goRunMapped(t, m)
	if got := runMapped(t, m); got != want {
		t.Fatalf("interp %q, go run %q", got, want)
	}
	if want != "hello, mapped\n" {
		t.Fatalf("go run = %q", want)
	}
}

// TestMappedInitOrder pins Go's cross-package initialisation order over the
// flat file: a dependency's variables and init functions run completely
// before its importer's initialisers, in map order, and the program's last.
func TestMappedInitOrder(t *testing.T) {
	m := mappedProgram{
		program: []Source{src("main.go", `package main

import (
	"fmt"

	"./b"
)

var mainVar = trace("main var", b.BVar+1)

func trace(what string, v int) int { fmt.Println(what, v); return v }

func init() { fmt.Println("main init", mainVar) }

func main() { fmt.Println("main", b.G()) }
`)},
		packages: []PackageSpec{
			{Path: "test/a", Sources: []Source{src("a.go", `package a

import "fmt"

var AVar = traceA("a var", 1)

func traceA(what string, v int) int { fmt.Println(what, v); return v }

func init() { fmt.Println("a init 1", AVar) }

func init() { fmt.Println("a init 2") }

func F() int { return AVar * 10 }
`)}},
			{Path: "test/b", Sources: []Source{src("b.go", `package b

import (
	"fmt"

	"./a"
)

var BVar = traceB("b var", a.F()+1)

func traceB(what string, v int) int { fmt.Println(what, v); return v }

func init() { fmt.Println("b init", BVar) }

func G() int { return BVar + a.AVar }
`)}},
		},
	}
	want := goRunMapped(t, m)
	if got := runMapped(t, m); got != want {
		t.Fatalf("interp:\n%sgo run:\n%s", got, want)
	}
	const pinned = "a var 1\na init 1 1\na init 2\nb var 11\nb init 11\nmain var 12\nmain init 12\nmain 12\n"
	if want != pinned {
		t.Fatalf("go run = %q, want %q", want, pinned)
	}
	prog, err := Load(m.program, m.options(true))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"__gosource_init_0", "__gosource_init_1", "__gosource_init_2", "__gosource_init_3"}; !reflect.DeepEqual(prog.InitFunctions, want) {
		t.Fatalf("InitFunctions = %q, want %q", prog.InitFunctions, want)
	}
}

// TestMappedNameCollisionRuns pins the lift of the M1 flat-namespace
// boundary: a package-level name declared by two linked packages, the
// program included and unexported names included, runs and prints what go
// run prints, because a mapped package's names are renamed under the
// hygiene prefix while the program keeps its own.
func TestMappedNameCollisionRuns(t *testing.T) {
	a := PackageSpec{Path: "test/a", Sources: []Source{src("a.go", "package a\n\nvar count = 1\n\nfunc F() int { return count }\n")}}
	for _, tc := range []struct {
		name string
		m    mappedProgram
		want string
	}{
		{"program-vs-package", mappedProgram{
			program:  []Source{src("main.go", "package main\n\nimport (\n\t\"fmt\"\n\n\t\"./a\"\n)\n\nvar count = 2\n\nfunc main() { fmt.Println(a.F(), count, a.F()+count) }\n")},
			packages: []PackageSpec{a},
		}, "1 2 3\n"},
		{"package-vs-package", mappedProgram{
			program:  []Source{src("main.go", "package main\n\nimport (\n\t\"fmt\"\n\n\t\"./a\"\n\t\"./b\"\n)\n\nfunc main() { fmt.Println(a.F(), b.G(), b.F{}) }\n")},
			packages: []PackageSpec{a, {Path: "test/b", Sources: []Source{src("b.go", "package b\n\ntype F struct{ N int }\n\nfunc G() int { return 0 }\n")}}},
		}, "1 0 {0}\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := goRunMapped(t, tc.m)
			if got := runMapped(t, tc.m); got != want {
				t.Fatalf("interp %q, go run %q", got, want)
			}
			if want != tc.want {
				t.Fatalf("go run = %q, want %q", want, tc.want)
			}
		})
	}
}

// TestMappedCollidingNamesAcrossTwoDependencies covers the shape the corpus
// collides on: two dependencies declaring the same function, type, method,
// variable and constant names, used side by side by the program through
// every spelling site — calls, composite literals, method calls on both
// types, inferred variable types, function values whose signatures name
// the types, conversions, pointers, a type switch over both, and a
// function-local type whose inferred spelling stays bare. (The local types
// are spelled apart: the runtime has no lexical type namespace, in one
// package or across the map.)
func TestMappedCollidingNamesAcrossTwoDependencies(t *testing.T) {
	dep := func(name string, base int) PackageSpec {
		return PackageSpec{Path: "test/" + name, Sources: []Source{src(name+".go", `package `+name+`

import "fmt"

const Base = `+strconv.Itoa(base)+`

var V = Base * 10

type T struct{ N int }

func New(n int) T { return T{N: n + Base} }

func (t T) Describe() string { return fmt.Sprintf("`+name+`.T(%d)", t.N) }

func (t *T) Bump() { t.N++ }

func F() int { return V + 1 }

type S string

func (s S) Tag() string { return "`+name+`:" + string(s) }

func Local() string {
	type L`+name+` struct{ N int }
	var l = L`+name+`{N: Base}
	return fmt.Sprint(l.N)
}
`)}}
	}
	m := mappedProgram{
		program: []Source{src("main.go", `package main

import (
	"fmt"

	"./a"
	"./b"
)

var va = a.New(1)

var vb = b.New(1)

func describe(x any) string {
	switch v := x.(type) {
	case a.T:
		return "a: " + v.Describe()
	case b.T:
		return "b: " + v.Describe()
	case a.S:
		return v.Tag()
	case b.S:
		return v.Tag()
	}
	return "?"
}

func main() {
	fmt.Println(a.F(), b.F(), a.V, b.V, a.Base, b.Base)
	fmt.Println(a.T{N: 5}.Describe(), b.T{N: 5}.Describe())
	fmt.Println(va.Describe(), vb.Describe())
	fa, fb := a.New, b.New
	fmt.Println(fa(2).Describe(), fb(2).Describe())
	pa, pb := &va, &vb
	pa.Bump()
	pb.Bump()
	pb.Bump()
	fmt.Println(pa.N, pb.N)
	fmt.Println(describe(va), describe(vb), describe(a.S("x")), describe(b.S("y")))
	fmt.Println(a.S("p").Tag(), b.S("q").Tag(), string(a.S("r")))
	fmt.Println(a.Local(), b.Local())
}
`)},
		packages: []PackageSpec{dep("a", 100), dep("b", 200)},
	}
	want := goRunMapped(t, m)
	if got := runMapped(t, m); got != want {
		t.Fatalf("interp:\n%sgo run:\n%s", got, want)
	}
	const pinned = "1001 2001 1000 2000 100 200\na.T(5) b.T(5)\na.T(101) b.T(201)\na.T(102) b.T(202)\n102 203\na: a.T(102) b: b.T(203) a:x b:y\na:p b:q r\n100 200\n"
	if want != pinned {
		t.Fatalf("go run = %q, want %q", want, pinned)
	}
}

// TestMappedProgramNameShadowsDependency pins that the program's own names
// are untouched when a dependency declares the same ones: main's I, T, F
// and global keep their spelling in the lowered file, and both sides run.
func TestMappedProgramNameShadowsDependency(t *testing.T) {
	m := mappedProgram{
		program: []Source{src("main.go", `package main

import (
	"fmt"

	"./dep"
)

type I interface{ Name() string }

type T struct{}

func (T) Name() string { return "main.T" }

var global = "main global"

func F() string { return "main F" }

func main() {
	var mine I = T{}
	var theirs dep.I = dep.T{}
	fmt.Println(mine.Name(), theirs.Name(), F(), dep.F(), global, dep.Global)
	var both []I = []I{mine, theirs}
	for _, i := range both {
		fmt.Println(i.Name())
	}
}
`)},
		packages: []PackageSpec{{Path: "test/dep", Sources: []Source{src("dep.go", `package dep

type I interface{ Name() string }

type T struct{}

func (T) Name() string { return "dep.T" }

var Global = "dep global"

var global = "dep unexported"

func F() string { return "dep F " + global }
`)}}},
	}
	want := goRunMapped(t, m)
	if got := runMapped(t, m); got != want {
		t.Fatalf("interp:\n%sgo run:\n%s", got, want)
	}
	if want != "main.T dep.T main F dep F dep unexported main global dep global\nmain.T\ndep.T\n" {
		t.Fatalf("go run = %q", want)
	}
	prog, err := Load(m.program, m.options(true))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, stmt := range prog.File.Stmts {
		switch d := stmt.Cmd.(type) {
		case *syntax.BashPPFuncDecl:
			if d.Receiver == nil {
				names[d.Name.Value] = true
			} else {
				names[d.Receiver.RecvType.Value+"."+d.Name.Value] = true
			}
		case *syntax.BashPPDecl:
			names[d.Name.Value] = true
		}
	}
	for _, name := range []string{"I", "T", "F", "global", "T.Name", "__gosource_pkg_0_I", "__gosource_pkg_0_T", "__gosource_pkg_0_F", "__gosource_pkg_0_global", "__gosource_pkg_0_Global", "__gosource_pkg_0_T.Name"} {
		if !names[name] {
			t.Errorf("%s missing from the lowered file; have %v", name, names)
		}
	}
}

// TestMappedEmbeddedTypeKeepsFieldName pins embedding across the map: an
// embedded mapped type is selected by its Go field name, promotes its fields
// and methods, and is addressable through a keyed composite literal, in the
// program and inside a mapped package alike, by pointer and by value.
func TestMappedEmbeddedTypeKeepsFieldName(t *testing.T) {
	m := mappedProgram{
		program: []Source{src("main.go", `package main

import (
	"fmt"

	"./a"
	"./b"
)

type Wrapper struct {
	a.Base
	Extra int
}

type PtrWrapper struct {
	*a.Base
}

func main() {
	w := Wrapper{Base: a.Base{ID: 1}, Extra: 2}
	fmt.Println(w.ID, w.Base.ID, w.Label(), w.Extra)
	w.Base.ID = 3
	w.ID++
	fmt.Println(w.ID, w.Label())
	p := PtrWrapper{Base: &a.Base{ID: 7}}
	fmt.Println(p.ID, p.Label(), p.Base.ID)
	d := b.Derived{Base: a.Base{ID: 9}, Name: "d"}
	fmt.Println(d.ID, d.Label(), d.Base.ID, d.Name, b.Show(d))
	var l a.Labeler = w
	fmt.Println(l.Label())
}
`)},
		packages: []PackageSpec{
			{Path: "test/a", Sources: []Source{src("a.go", `package a

import "fmt"

type Base struct{ ID int }

func (b Base) Label() string { return fmt.Sprintf("base#%d", b.ID) }

type Labeler interface{ Label() string }
`)}},
			{Path: "test/b", Sources: []Source{src("b.go", `package b

import "./a"

type Derived struct {
	a.Base
	Name string
}

func Show(d Derived) string { return d.Name + "/" + d.Label() }
`)}},
		},
	}
	want := goRunMapped(t, m)
	if got := runMapped(t, m); got != want {
		t.Fatalf("interp:\n%sgo run:\n%s", got, want)
	}
	if want != "1 1 base#1 2\n4 base#4\n7 base#7 7\n9 base#9 9 d d/base#9\nbase#4\n" {
		t.Fatalf("go run = %q", want)
	}
}

// TestMappedEmbedIsRefused pins the M1 boundary on go:embed inside a mapped
// package: directives are attached for the program's files only.
func TestMappedEmbedIsRefused(t *testing.T) {
	m := mappedProgram{
		program:  []Source{src("main.go", "package main\n\nimport (\n\t\"fmt\"\n\n\t\"./a\"\n)\n\nfunc main() { fmt.Println(a.Text) }\n")},
		packages: []PackageSpec{{Path: "test/a", Sources: []Source{src("a.go", "package a\n\nimport _ \"embed\"\n\n//go:embed a.go\nvar Text string\n")}}},
	}
	_, err := Load(m.program, m.options(true))
	want := `a.go:5:1: gosource: go:embed in mapped package "test/a" is not supported by the explicit package map`
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v\nwant %s", err, want)
	}
}

func TestDotImportsStayFileScopedInFlatCompile(t *testing.T) {
	sources := []Source{
		src("a.go", `package p

import . "strings"

var _ = Index

type T struct{ Index int }

var _ = T{Index: 0}
`),
		src("b.go", `package p

import . "bytes"

var _ = Index

var _ = T{Index: 0}
`),
	}
	program, err := Load(sources, Options{ImportPath: "p"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := lower.Compile(program.File, lower.Options{Package: program.Package, Importer: program.Importer, Origin: "generated.go"})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/dotimport\n\ngo 1.23\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(result.Source), "func Index(") {
		t.Fatalf("lowered a dot-imported name as a declaration:\n%s", result.Source)
	}
	if err := os.WriteFile(filepath.Join(root, "generated.go"), result.Source, 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "test", "./...")
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated package failed: %v\n%s", err, out)
	}
}

func TestDuplicateTopLevelNameStillRefused(t *testing.T) {
	_, err := Load([]Source{
		src("a.go", "package p\n\nvar x int\n"),
		src("b.go", "package p\n\nvar x int\n"),
	}, Options{PreserveNativeInit: true})
	if err == nil || !strings.Contains(err.Error(), "x redeclared in this block") {
		t.Fatalf("err = %v", err)
	}
}

// TestMappedTypeNameKnownDifference documents the limit that remains after
// name mangling: the flat file spells every linked type under the program's
// package, and a mapped type by its rename, so a mapped a.T prints as
// main.__gosource_pkg_0_T where go run prints a.T. Pinned, not hidden; a
// display name for the type would have to be registered by the native
// dependency bridge, which mirrors the type into its helper under that name.
func TestMappedTypeNameKnownDifference(t *testing.T) {
	m := mappedProgram{
		program: []Source{src("main.go", `package main

import (
	"fmt"

	"./a"
)

func main() {
	var v a.T = a.New(3)
	fmt.Printf("%T %v %d\n", v, v, v.N)
	fmt.Println(a.Describe(v))
}
`)},
		packages: []PackageSpec{{Path: "test/a", Sources: []Source{src("a.go", `package a

import "fmt"

type T struct{ N int }

func New(n int) T { return T{N: n} }

func Describe(t T) string { return fmt.Sprintf("%T", t) }
`)}}},
	}
	if want, got := "a.T {3} 3\na.T\n", goRunMapped(t, m); got != want {
		t.Fatalf("go run = %q, want %q", got, want)
	}
	if want, got := "main.__gosource_pkg_0_T {3} 3\nmain.__gosource_pkg_0_T\n", runMapped(t, m); got != want {
		t.Fatalf("interp = %q, want the pinned known difference %q (if a mapped type now keeps its package, update this test and FINDINGS-M1.md)", got, want)
	}
}

// TestMappedDiamondImportLinksOnce pins that a package imported by two linked
// packages (b→a, c→a, main→b,c) is lowered once: one linked entry, one
// function definition, one initialised variable — each under its mapped
// rename, by map index — and go run's output.
func TestMappedDiamondImportLinksOnce(t *testing.T) {
	m := mappedProgram{
		program: []Source{src("main.go", `package main

import (
	"fmt"

	"./b"
	"./c"
)

func main() { fmt.Println(b.G(), c.H(), b.G()+c.H()) }
`)},
		packages: []PackageSpec{
			{Path: "test/a", Sources: []Source{src("a.go", "package a\n\nimport \"fmt\"\n\nvar Counter = next()\n\nvar calls int\n\nfunc next() int { calls++; fmt.Println(\"a initialised\", calls); return calls }\n\nfunc F() int { return Counter }\n")}},
			{Path: "test/b", Sources: []Source{src("b.go", "package b\n\nimport \"./a\"\n\nfunc G() int { return a.F() + 1 }\n")}},
			{Path: "test/c", Sources: []Source{src("c.go", "package c\n\nimport \"./a\"\n\nfunc H() int { return a.F() + 2 }\n")}},
		},
	}
	want := goRunMapped(t, m)
	if got := runMapped(t, m); got != want {
		t.Fatalf("interp %q, go run %q", got, want)
	}
	if want != "a initialised 1\n2 3 5\n" {
		t.Fatalf("go run = %q", want)
	}
	prog, err := Load(m.program, m.options(true))
	if err != nil {
		t.Fatal(err)
	}
	if len(prog.Packages) != 3 || prog.Packages[0].Path != "test/a" {
		t.Fatalf("Packages = %#v", prog.Packages)
	}
	defs := map[string]int{}
	for _, stmt := range prog.File.Stmts {
		switch d := stmt.Cmd.(type) {
		case *syntax.BashPPFuncDecl:
			defs[d.Name.Value]++
		case *syntax.BashPPDecl:
			defs[d.Name.Value]++
		}
	}
	for _, name := range []string{"__gosource_pkg_0_F", "__gosource_pkg_0_next", "__gosource_pkg_0_Counter", "__gosource_pkg_0_calls", "__gosource_pkg_1_G", "__gosource_pkg_2_H"} {
		if defs[name] != 1 {
			t.Fatalf("%s defined %d times in the lowered file", name, defs[name])
		}
	}
}

// TestMappedSelectorSites covers every site the converter collapses a mapped
// qualifier at: types in struct fields, pointers, parameters and results
// (typ and text), composite literals, method calls on mapped types, function
// values, constants, variadics, interface satisfaction across packages,
// generics, and a blank import of a mapped package whose init still runs.
func TestMappedSelectorSites(t *testing.T) {
	m := mappedProgram{
		program: []Source{src("main.go", `package main

import (
	"fmt"

	"./a"
	_ "./z"
)

type Holder struct {
	Item  a.T
	Items []*a.T
	M     map[string]a.T
}

func use(t a.T) a.T { return t }

func main() {
	h := Holder{Item: a.T{N: 1}, Items: []*a.T{&a.T{N: 2}}, M: map[string]a.T{"k": a.T{N: 3}}}
	var p *a.T = &h.Item
	fmt.Println(h.Item.Double(), h.Items[0].N, h.M["k"].N, p.N, use(a.T{N: 4}).N, a.Kind, a.K2)
	fn := a.Double
	fmt.Println(fn(a.T{N: 5}), a.Sum(1, 2, 3))
	var s a.Shape = a.Square{Side: 2}
	fmt.Println(s.Area())
	fmt.Println(a.Generic[int](7))
}
`)},
		packages: []PackageSpec{
			{Path: "test/a", Sources: []Source{src("a.go", `package a

type T struct{ N int }

const Kind = "kind"

const K2 = 2

func (t T) Double() int { return t.N * 2 }

func Double(t T) int { return t.Double() }

func Sum(xs ...int) int {
	s := 0
	for _, x := range xs {
		s += x
	}
	return s
}

type Shape interface{ Area() int }

type Square struct{ Side int }

func (s Square) Area() int { return s.Side * s.Side }

func Generic[X any](x X) X { return x }
`)}},
			{Path: "test/z", Sources: []Source{src("z.go", "package z\n\nimport \"fmt\"\n\nfunc init() { fmt.Println(\"z init\") }\n")}},
		},
	}
	want := goRunMapped(t, m)
	if got := runMapped(t, m); got != want {
		t.Fatalf("interp:\n%sgo run:\n%s", got, want)
	}
	if want != "z init\n2 2 3 1 4 kind 2\n10 6\n4\n7\n" {
		t.Fatalf("go run = %q", want)
	}
}
