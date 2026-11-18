package lower_test

import (
	"bytes"
	"fmt"
	"go/importer"
	"go/types"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/lower"
)

type fixtureImporter struct {
	packages map[string]*types.Package
	fallback types.Importer
}

func (i fixtureImporter) Import(path string) (*types.Package, error) {
	if pkg := i.packages[path]; pkg != nil {
		return pkg, nil
	}
	if i.fallback != nil {
		return i.fallback.Import(path)
	}
	return nil, fmt.Errorf("fixture importer has no package %q", path)
}

// TestGoSourceSecondLoweringDoesNotGrowRangeFunctions is the outside-corpus
// control for a transpile consumer which has to lower its generated Go again.
// A range function must remain native Go on both passes; in particular the
// source-map scaffolding must not turn each pass into a larger program.
func TestGoSourceSecondLoweringDoesNotGrowRangeFunctions(t *testing.T) {
	path := filepath.Join("testdata", "sprint165", "range-function", "main.go")
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	firstProgram, err := gosource.Parse(strings.NewReader(string(source)), path, gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	first, err := lower.Compile(firstProgram.File, lower.Options{Origin: path})
	if err != nil {
		t.Fatal(err)
	}
	secondProgram, err := gosource.Parse(strings.NewReader(string(first.Source)), "generated.go", gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	second, err := lower.Compile(secondProgram.File, lower.Options{Origin: "generated.go"})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Source) > len(first.Source) {
		t.Fatalf("second lowering grew range source from %d to %d bytes", len(first.Source), len(second.Source))
	}
	if !strings.Contains(string(second.Source), "for n := range values") {
		t.Fatalf("second lowering rewrote native range function:\n%s", second.Source)
	}
}

// TestGoSourceLibraryEmissionKeepsPackageIdentity drives the compiled
// multi-package shape without flattening source packages into a prefixed main
// package. It covers the common emitter contract behind package-qualified
// interface values and dependency init frames: package identity belongs to
// the emitted package clause, not a rewritten declaration name.
func TestGoSourceLibraryEmissionKeepsPackageIdentity(t *testing.T) {
	fixtures := filepath.Join("testdata", "sprint165", "package-identity")
	buildDir := t.TempDir()
	write := func(name string, data []byte) {
		t.Helper()
		path := filepath.Join(buildDir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", []byte("module example/packageidentity\n\ngo 1.25\n"))

	lowerFile := func(name, pkg string, importer types.Importer) {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(fixtures, name))
		if err != nil {
			t.Fatal(err)
		}
		program, err := gosource.Parse(bytes.NewReader(data), name, gosource.Options{PreserveNativeInit: true, Importer: importer})
		if err != nil {
			t.Fatal(err)
		}
		result, err := lower.Compile(program.File, lower.Options{Package: pkg, Library: true, Dir: buildDir, Importer: program.Importer})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Files) != 1 || result.Files[0].Name != name {
			t.Fatalf("%s library result = %#v", name, result.Files)
		}
		write(name, result.Files[0].Source)
	}
	lowerFile(filepath.Join("a", "a.go"), "a", nil)
	dep := types.NewPackage("example/packageidentity/a", "a")
	dep.Scope().Insert(types.NewTypeName(0, dep, "Item", types.NewNamed(types.NewTypeName(0, dep, "Item", nil), types.NewStruct(nil, nil), nil)))
	dep.MarkComplete()
	lowerFile("main.go", "main", fixtureImporter{packages: map[string]*types.Package{"example/packageidentity/a": dep}, fallback: importer.Default()})

	cmd := exec.Command("go", "run", ".")
	cmd.Dir = buildDir
	cmd.Env = append(os.Environ(), "GOWORK=off")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("generated package run: %v\n%s", err, output)
	}
	if got, want := string(output), "a.init\na.Item\n"; got != want {
		t.Fatalf("generated package output = %q, want %q", got, want)
	}
}
