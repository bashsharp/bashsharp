//go:build full

package interp_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/lower"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Sprint: #249; Story: #715; Story-ID: 90f96d4f4dae
func TestGoSourceS249LocalCgoSubpackage(t *testing.T) {
	dir := t.TempDir()
	mainSource := []byte(`package main

import "example.com/cgosubpackage/bad"

func main() { bad.Bad() }
`)
	badSource := []byte(`package bad

/*
#include <stdlib.h>
*/
import "C"

type A struct{ B }
type B struct{ C1, C2 C }
type C struct{ D1, D2 D }
type D struct{ Bytes []byte }

func Bad() {
	zero := map[string]A{}["missing"]
	if len(zero.B.C1.D1.Bytes) != 0 || len(zero.B.C2.D2.Bytes) != 0 {
		panic("map miss did not produce a zero value")
	}
	p := C.malloc(100)
	if p == nil {
		panic("malloc failed")
	}
	C.free(p)
}
`)
	write := func(name string, data []byte) {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", []byte("module example.com/cgosubpackage\n\ngo 1.27\n"))
	write("main.go", mainSource)
	write("bad/bad.go", badSource)

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	native := exec.CommandContext(ctx, "go", "run", ".")
	native.Dir = dir
	if output, err := native.CombinedOutput(); err != nil {
		t.Fatalf("native control: %v: %s", err, output)
	}

	program, err := gosource.Load([]gosource.Source{{Name: filepath.Join(dir, "main.go"), Data: mainSource}}, gosource.Options{
		RunMain:    true,
		ImportPath: "example.com/cgosubpackage",
		Packages: []gosource.PackageSpec{{
			Path:    "example.com/cgosubpackage/bad",
			Sources: []gosource.Source{{Name: filepath.Join(dir, "bad/bad.go"), Data: badSource}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Run("interpreted", func(t *testing.T) {
		var stderr bytes.Buffer
		runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(dir), interp.GoSourceModuleDir(dir), interp.StdIO(nil, nil, &stderr))
		if err != nil {
			t.Fatal(err)
		}
		if err := runner.Run(ctx, program.File); err != nil {
			t.Fatalf("Runner: %v: %s", err, stderr.String())
		}
	})

	t.Run("compiled", func(t *testing.T) {
		result, err := lower.Compile(program.File, lower.Options{Package: program.Package, Dir: dir, Importer: program.Importer})
		if err != nil {
			t.Fatal(err)
		}
		compiledDir := filepath.Join(dir, "compiled")
		root, err := filepath.Abs("../..")
		if err != nil {
			t.Fatal(err)
		}
		write(filepath.Join("compiled", "go.mod"), []byte("module example.com/cgosubpackage/compiled\n\ngo 1.27\n\nrequire github.com/bashsharp/bashsharp v0.0.0\nreplace github.com/bashsharp/bashsharp => "+root+"\n"))
		write(filepath.Join("compiled", "main.go"), result.Source)
		compiled := exec.CommandContext(ctx, "go", "run", ".")
		compiled.Dir = compiledDir
		if output, err := compiled.CombinedOutput(); err != nil {
			t.Fatalf("compiled control: %v: %s\n--- generated ---\n%s", err, output, result.Source)
		}
	})

	t.Run("native-unit", func(t *testing.T) {
		unit, err := gosource.Load([]gosource.Source{{Name: filepath.Join(dir, "bad/bad.go"), Data: badSource}}, gosource.Options{
			PreserveNativeInit: true,
			ImportPath:         "example.com/cgosubpackage/bad",
		})
		if err != nil {
			t.Fatal(err)
		}
		result, err := lower.Compile(unit.File, lower.Options{Package: unit.Package, Library: true, Dir: filepath.Join(dir, "bad"), Importer: unit.Importer})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Files) != 1 {
			t.Fatalf("native unit files = %d", len(result.Files))
		}
		generated := string(result.Files[0].Source)
		for _, want := range []string{`import "C"`, "type C struct", "C.malloc(100)", "C.free(p)"} {
			if !strings.Contains(generated, want) {
				t.Fatalf("native unit lost %q:\n%s", want, generated)
			}
		}
		write(filepath.Join("nativeunit", "go.mod"), []byte("module example.com/cgosubpackage/nativeunit\n\ngo 1.27\n"))
		write(filepath.Join("nativeunit", "bad.go"), result.Files[0].Source)
		compiled := exec.CommandContext(ctx, "go", "build", ".")
		compiled.Dir = filepath.Join(dir, "nativeunit")
		if output, err := compiled.CombinedOutput(); err != nil {
			t.Fatalf("native-unit build: %v: %s\n--- generated ---\n%s", err, output, result.Files[0].Source)
		}
	})
}
