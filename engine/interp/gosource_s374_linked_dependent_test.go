//go:build full

package interp_test

// Sprint: #374; Story-ID: 5568f906f766

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/lower"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// The types2 test hands its importer cache, a map[string]*types2.Package, to
// cmd/compile/internal/importer. With types2 interpreted and the importer
// compiled against the on-disk types2, the map was refused at the bridge
// ("map[string]*main.__gosource_pkg_0_Package not assignable to
// map[string]*types2.Package"): two copies of one package. The dependent is
// now linked from source into the same program, so the map, the packages the
// dependent stores in it and the methods called on them are all one package's.
func TestS374DependentOfLinkedPackageRunsAgainstTheSameCopy(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"go.mod": "module example.com/dual\n\ngo 1.27\n",
		"p/p.go": `package p

type Package struct{ name string }

func NewPackage(name string) *Package { return &Package{name: name} }

func (p *Package) Name() string { return p.name }

func Count(packages map[string]*Package) int { return len(packages) }

func First(packages map[string]*Package, path string) string { return packages[path].name }
`,
		"dep/dep.go": `package dep

import "example.com/dual/p"

func Import(packages map[string]*p.Package, path string) *p.Package {
	if pkg := packages[path]; pkg != nil {
		return pkg
	}
	pkg := p.NewPackage(path)
	packages[path] = pkg
	return pkg
}
`,
		"main.go": `package main

import (
	"fmt"

	"example.com/dual/dep"
	"example.com/dual/p"
)

func main() {
	packages := make(map[string]*p.Package)
	pkg := dep.Import(packages, "fmt")
	again := dep.Import(packages, "fmt")
	fmt.Println(pkg.Name(), p.Count(packages), p.First(packages, "fmt"), pkg == again)
}
`,
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
	program, err := gosource.Load(
		[]gosource.Source{{Name: filepath.Join(dir, "main.go"), Data: []byte(files["main.go"])}},
		gosource.Options{
			RunMain:    true,
			ImportPath: "example.com/dual/cmd",
			Importer:   lower.NewModuleImporter(dir),
			Packages: []gosource.PackageSpec{{Path: "example.com/dual/p", SourceDir: filepath.Join(dir, "p"), Sources: []gosource.Source{
				{Name: filepath.Join(dir, "p", "p.go"), Data: []byte(files["p/p.go"])},
			}}},
		})
	if err != nil {
		t.Fatal(err)
	}
	var out, errs bytes.Buffer
	r, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(dir), interp.GoSourceModuleDir(dir),
		interp.GoSourceIdentity("example.com/dual/cmd", false), interp.StdIO(nil, &out, &errs))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	err = r.Run(ctx, program.File)
	if err != nil || out.String() != "fmt 1 fmt true\n" || errs.String() != "" {
		t.Fatalf("err=%v stdout=%q stderr=%q", err, out.String(), errs.String())
	}
}
