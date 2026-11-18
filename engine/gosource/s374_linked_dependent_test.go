package gosource_test

// Sprint: #374; Story-ID: 5568f906f766

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/lower"
)

// A mapped package p runs interpreted. A dependency that imports p is, on
// disk, compiled against its own copy of p, so at run time there would be two
// packages p: values of p's types cannot cross between them. cmd/go rebuilds
// such a dependent against the test variant; Load links it from its own
// source into the same program, so one p exists.
const s374DualP = `package p

type Package struct{ name string }

func NewPackage(name string) *Package { return &Package{name: name} }

func (p *Package) Name() string { return p.name }

func Count(packages map[string]*Package) int { return len(packages) }
`

// exposes p.Package in its API: must be linked.
const s374DualDep = `package dep

import "example.com/dual/p"

func Import(packages map[string]*p.Package, path string) *p.Package {
	pkg := p.NewPackage(path)
	packages[path] = pkg
	return pkg
}
`

// reaches p but exposes none of its types: stays compiled.
const s374DualQuiet = `package quiet

import "example.com/dual/p"

func Len(name string) int { return len(p.NewPackage(name).Name()) }
`

const s374DualMain = `package main

import (
	"fmt"

	"example.com/dual/dep"
	"example.com/dual/p"
	"example.com/dual/quiet"
)

func main() {
	packages := make(map[string]*p.Package)
	pkg := dep.Import(packages, "fmt")
	fmt.Println(pkg.Name(), p.Count(packages), quiet.Len("fmt"))
}
`

func s374DualModule(t *testing.T, extra map[string]string) (string, []gosource.Source, []gosource.PackageSpec) {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"go.mod":         "module example.com/dual\n\ngo 1.27\n",
		"p/p.go":         s374DualP,
		"dep/dep.go":     s374DualDep,
		"quiet/quiet.go": s374DualQuiet,
		"main.go":        s374DualMain,
	}
	for name, text := range extra {
		files[name] = text
	}
	for name, text := range files {
		path := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	sources := []gosource.Source{{Name: filepath.Join(dir, "main.go"), Data: []byte(files["main.go"])}}
	packages := []gosource.PackageSpec{{Path: "example.com/dual/p", SourceDir: filepath.Join(dir, "p"), Sources: []gosource.Source{
		{Name: filepath.Join(dir, "p", "p.go"), Data: []byte(files["p/p.go"])},
	}}}
	return dir, sources, packages
}

func s374LinkedPaths(program *gosource.Program) []string {
	var paths []string
	for _, pkg := range program.Packages {
		paths = append(paths, pkg.Path)
	}
	return paths
}

func s374Resolution(program *gosource.Program, path string) gosource.Resolution {
	for _, resolution := range program.Resolutions {
		if resolution.Path == path {
			return resolution
		}
	}
	return gosource.Resolution{}
}

// The test never changes directory: the dependent's source is listed from the
// module the program lives in, not from the process working directory.
func TestS374DependentExposingMappedTypesIsLinked(t *testing.T) {
	dir, sources, packages := s374DualModule(t, nil)
	program, err := gosource.Load(sources, gosource.Options{
		RunMain: true, ImportPath: "example.com/dual/cmd",
		Importer: lower.NewModuleImporter(dir), Packages: packages,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := s374LinkedPaths(program), []string{"example.com/dual/p", "example.com/dual/dep"}; !slices.Equal(got, want) {
		t.Fatalf("linked packages = %v, want %v", got, want)
	}
	if got := s374Resolution(program, "example.com/dual/dep"); got.Origin != "linked-dependent" || got.Note != "" || len(got.Files) != 1 {
		t.Fatalf("dep resolution = %+v", got)
	}
	if got := s374Resolution(program, "example.com/dual/quiet"); got.Origin != "importer" || got.Note != "" {
		t.Fatalf("quiet resolution = %+v", got)
	}
}

// A native unit links nothing: the dependent keeps its re-checked types and
// its compiled form, as before.
func TestS374NativeUnitLinksNoDependent(t *testing.T) {
	dir, sources, packages := s374DualModule(t, nil)
	program, err := gosource.Load(sources, gosource.Options{
		ImportPath: "example.com/dual/cmd", PreserveNativeInit: true,
		Importer: lower.NewModuleImporter(dir), Packages: packages,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := s374LinkedPaths(program); len(got) != 0 {
		t.Fatalf("linked packages = %v, want none", got)
	}
	if got := s374Resolution(program, "example.com/dual/dep"); got.Origin != "importer" || got.Note != "" {
		t.Fatalf("dep resolution = %+v", got)
	}
}

// A dependent that cannot be linked from Go source alone keeps its compiled
// form, and the resolution says so instead of leaving two copies unannounced.
func TestS374UnlinkableDependentIsReported(t *testing.T) {
	dir, sources, packages := s374DualModule(t, map[string]string{"dep/stub.s": "// assembly input\n"})
	program, err := gosource.Load(sources, gosource.Options{
		RunMain: true, ImportPath: "example.com/dual/cmd",
		Importer: lower.NewModuleImporter(dir), Packages: packages,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := s374LinkedPaths(program), []string{"example.com/dual/p"}; !slices.Equal(got, want) {
		t.Fatalf("linked packages = %v, want %v", got, want)
	}
	got := s374Resolution(program, "example.com/dual/dep")
	if got.Origin != "importer" || !strings.Contains(got.Note, "non-Go inputs") || !strings.Contains(got.Note, "example.com/dual/p.Package") {
		t.Fatalf("dep resolution = %+v", got)
	}
}
