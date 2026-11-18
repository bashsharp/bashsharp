package lower_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/lower"
)

// TestGoSourceDotImportsKeepEveryPath is the reproducer behind the Sprint
// 171 gosource-units request: a dot or blank import binds no identifier, so
// the emitter keyed two `import . "..."` lines under one alias and the second
// replaced the first; the corpus canary bug191 (two dot-imported mapped
// packages) regressed once a directory phase lowered to a native unit.
func TestGoSourceDotImportsKeepEveryPath(t *testing.T) {
	path := filepath.Join("testdata", "sprint171", "manager", "dot_imports.go")
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	program, err := gosource.Parse(bytes.NewReader(source), path, gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	result, err := lower.Compile(program.File, lower.Options{Origin: path})
	if err != nil {
		t.Fatal(err)
	}
	generated := string(result.Source)
	for _, want := range []string{`import . "fmt"`, `import . "strings"`, `import _ "embed"`, `import _ "unsafe"`} {
		if !strings.Contains(generated, want) {
			t.Errorf("generated Go lost %s:\n%s", want, generated)
		}
	}
}

// TestGoSourceInferredConstTypeIsNotSpelled pins the go/types resolver.go
// shape found by the Sprint 171 follow-up leaf: a constant with no written
// type whose initializer names a dot-imported constant of a named type.
// The converter spells the inferred type (`time.Duration`) for the
// interpreter; generated Go must not, since the file binds the package by
// dot import and gc infers the type from the written initializer anyway.
func TestGoSourceInferredConstTypeIsNotSpelled(t *testing.T) {
	path := filepath.Join("testdata", "sprint171", "manager", "dot-const", "lib.go")
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	program, err := gosource.Load([]gosource.Source{{Name: "lib.go", Data: source}}, gosource.Options{PreserveNativeInit: true})
	if err != nil {
		t.Fatal(err)
	}
	result, err := lower.Compile(program.File, lower.Options{Package: program.Package, Library: true, Importer: program.Importer})
	if err != nil {
		t.Fatalf("library did not lower: %v", err)
	}
	generated := string(result.Files[0].Source)
	for _, want := range []string{"const unit = Millisecond", "first  = Second", "second = first * 2", `import . "time"`} {
		if !strings.Contains(generated, want) {
			t.Errorf("generated unit lost %q:\n%s", want, generated)
		}
	}
	if strings.Contains(generated, "time.Duration") {
		t.Errorf("generated unit spells the inferred type:\n%s", generated)
	}
}
