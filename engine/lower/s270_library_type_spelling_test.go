package lower_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/lower"
)

// buildLibrary lowers sources as one native library unit and builds the
// emitted per-file Go with gc.
func buildLibrary(t *testing.T, sources []gosource.Source) map[string]string {
	t.Helper()
	program, err := gosource.Load(sources, gosource.Options{PreserveNativeInit: true})
	if err != nil {
		t.Fatal(err)
	}
	result, err := lower.Compile(program.File, lower.Options{Package: program.Package, Library: true, Importer: program.Importer})
	if err != nil {
		t.Fatalf("library transpile refused the package: %v", err)
	}
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "go.mod"), []byte("module example.com/fixture\n\ngo 1.27\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	generated := map[string]string{}
	for _, file := range result.Files {
		generated[file.Name] = string(file.Source)
		if err := os.WriteFile(filepath.Join(work, filepath.Base(file.Name)), file.Source, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "vet", ".")
	cmd.Dir = work
	cmd.Env = append(os.Environ(), "GOWORK=off")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("gc refused the generated package: %v\n%s", err, out)
	}
	return generated
}

// A typed constant's contextual type is spelled with the qualifier the
// CURRENT file binds for the package (cmd/compile/internal/noder, dwarfgen):
// an alias another file minted for the same path is not in scope once files
// are emitted separately.
func TestS270LibraryConstantTypeUsesItsFileBinding(t *testing.T) {
	buildLibrary(t, []gosource.Source{
		{Name: "a.go", Data: []byte("package fixture\n\nimport errors \"errors\"\n\nvar E = errors.New(\"x\")\n")},
		{Name: "b.go", Data: []byte("package fixture\n\nimport errors \"io/fs\"\n\nfunc B(m errors.FileMode) bool { return m&errors.ModeDir != 0 }\n")},
		{Name: "c.go", Data: []byte("package fixture\n\nimport errors \"io/fs\"\n\nfunc C(m errors.FileMode) bool { return m&errors.ModeSymlink != 0 }\n")},
	})
}

// A file that never imports the type's package (cmd/compile/internal/liveness,
// go/types' token) gets a synthetic import it owns, not another file's name.
func TestS270LibraryConstantTypeInFileWithoutImport(t *testing.T) {
	buildLibrary(t, []gosource.Source{
		{Name: "a.go", Data: []byte("package fixture\n\nimport \"io/fs\"\n\nfunc Mode() fs.FileMode { return fs.ModeDir }\n")},
		{Name: "b.go", Data: []byte("package fixture\n\nfunc Dir() bool {\n\tm := Mode()\n\tm = 0\n\treturn m == 0\n}\n")},
	})
}

// A type spelled only for the lowering (a call result type in a file whose Go
// output keeps the source form, cmd/compile/internal/base) must not leave an
// unused synthetic import in the emitted file.
func TestS270LibraryUnusedSyntheticImportDropped(t *testing.T) {
	buildLibrary(t, []gosource.Source{
		{Name: "a.go", Data: []byte("package fixture\n\nimport \"os\"\n\nvar fsys = os.DirFS(\".\")\n\nfunc F() any { return fsys }\n")},
	})
}

// A local binding that shadows the constant's type name (a range variable
// named rune, a local variable named after its own type) must not be called
// as a conversion (cmd/internal/testdir, cmd/compile/internal/ssa).
func TestS270LibraryShadowedConversionName(t *testing.T) {
	generated := buildLibrary(t, []gosource.Source{
		{Name: "a.go", Data: []byte(`package fixture

type branch int

const (
	unknown branch = iota
	positive
)

func count(s string) (n int) {
	for _, rune := range s {
		if rune == '\\' {
			n++
		}
	}
	return n
}

func pick() bool {
	branch := positive
	if branch == 1 {
		return true
	}
	return branch == unknown
}
`)},
	})
	if got := generated["a.go"]; strings.Contains(got, "rune(") || strings.Contains(got, "branch(1)") {
		t.Errorf("a shadowed type name is spelled as a conversion\n--- generated\n%s", got)
	}
}

// A constant of another package's unexported type (types.CountBlankFields in
// cmd/compile/internal/ssagen, test/fixedbugs/issue24801's a.X = 1) keeps its
// source form: the use site cannot name the type.
func TestS270UnexportedForeignConstantType(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) gosource.Source {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return gosource.Source{Name: path, Data: []byte(body)}
	}
	a := write("a.go", "package a\n\ntype main int\n\nvar X main\n")
	m := write("main.go", "package main\n\nimport \"./a\"\n\nfunc main() {\n\ta.X = 1\n}\n")
	program, err := gosource.Load([]gosource.Source{m}, gosource.Options{
		Importer:           lower.NewModuleImporter(dir),
		ImportBase:         "test",
		ImportPath:         "main",
		PreserveNativeInit: true,
		Packages:           []gosource.PackageSpec{{Path: "test/a", Sources: []gosource.Source{a}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := lower.Compile(program.File, lower.Options{Package: program.Package, Importer: program.Importer})
	if err != nil {
		t.Fatalf("lowering refused the package: %v", err)
	}
	if got := string(result.Source); strings.Contains(got, "a.main(") {
		t.Errorf("the unexported foreign type is spelled\n--- generated\n%s", got)
	}
}

// A pointer method of an imported type called on an indexed array element
// (cmd/compile/internal/ssa's poolFree[b-5].Get()) binds the element's
// address, never a copy: vet refuses the copied lock, and Put into a copy is
// lost.
func TestS270ComputedPointerMethodReceiverIsAddressed(t *testing.T) {
	generated := buildLibrary(t, []gosource.Source{
		{Name: "a.go", Data: []byte(`package fixture

import "sync"

var pools [4]sync.Pool

func Round(b int, v any) any {
	pools[b-1].Put(v)
	return pools[b-1].Get()
}
`)},
	})
	if got := generated["a.go"]; !strings.Contains(got, "&pools[b-1]") {
		t.Errorf("the element receiver is not addressed\n--- generated\n%s", got)
	}
}
