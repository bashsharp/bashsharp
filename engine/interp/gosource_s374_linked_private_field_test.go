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

// A linked package reads an unexported field of a value of its own type that
// a compiled dependency produced: go/types validates `imp.name` on the
// *Package its Config.Importer returned (resolver.go). The worker grants the
// read only to the field's declaring import path, so the interpreter must
// send the path the linked source was authenticated under. It sent the
// linker's hygiene tag ("0") instead, which matches no package, and every such
// read was refused as "unexported field name".
func TestS374LinkedPackageReadsOwnPrivateFieldOfDependencyValue(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"go.mod": "module example.com/dual\n\ngo 1.27\n",
		"p/p.go": `package p

type Package struct {
	name string
}

func NewPackage(name string) *Package { return &Package{name: name} }

type Importer interface {
	Import(path string) (*Package, error)
}

func Resolve(importer Importer, path string) (string, error) {
	imp, err := importer.Import(path)
	if err == nil && imp != nil && (imp.name == "_" || imp.name == "") {
		return "", nil
	}
	return imp.name, err
}
`,
		// An assembly input keeps dep on its compiled form, so the value it
		// returns is the on-disk package's and the read crosses the bridge.
		"dep/stub.s": "// assembly input\n",
		"dep/dep.go": `package dep

import "example.com/dual/p"

type cache struct{}

func (cache) Import(path string) (*p.Package, error) { return p.NewPackage(path), nil }

func Default() p.Importer { return cache{} }
`,
		"main.go": `package main

import (
	"fmt"

	"example.com/dual/dep"
	"example.com/dual/p"
)

func main() {
	fmt.Println(p.Resolve(dep.Default(), "fmt"))
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
	if err != nil || out.String() != "fmt <nil>\n" || errs.String() != "" {
		t.Fatalf("err=%v stdout=%q stderr=%q", err, out.String(), errs.String())
	}
}
