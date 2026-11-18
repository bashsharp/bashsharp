package lower_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/lower"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestGoSourceLibraryEmission(t *testing.T) {
	dir := filepath.Join("testdata", "sprint162", "library")
	load := func(names ...string) *gosource.Program {
		t.Helper()
		var sources []gosource.Source
		for _, name := range names {
			data, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			sources = append(sources, gosource.Source{Name: name, Data: data})
		}
		program, err := gosource.Load(sources, gosource.Options{PreserveNativeInit: true})
		if err != nil {
			t.Fatal(err)
		}
		return program
	}
	emit := func(program *gosource.Program) *lower.Result {
		t.Helper()
		result, err := lower.Compile(program.File, lower.Options{Package: program.Package, Library: true, Importer: program.Importer})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}

	regular := emit(load("a.go", "b_test.go"))
	external := emit(load("external_test.go"))
	if regular.Source != nil || regular.Entry != "" || external.Source != nil || external.Entry != "" {
		t.Fatalf("library result exposed a flat source or runtime entry")
	}
	if len(regular.Files) != 2 || len(external.Files) != 1 {
		t.Fatalf("emitted regular/external files = %d/%d, want 2/1", len(regular.Files), len(external.Files))
	}

	buildDir := t.TempDir()
	write := func(name string, data []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(buildDir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", []byte("module example/library\n\ngo 1.23\n"))
	for _, result := range []*lower.Result{regular, external} {
		for _, output := range result.Files {
			original, err := os.ReadFile(filepath.Join(dir, output.Name))
			if err != nil {
				t.Fatal(err)
			}
			if want, got := fidelityNormalize(t, original), fidelityNormalize(t, output.Source); want != got {
				t.Errorf("%s does not retain Go-only identity\n--- want\n%s\n--- got\n%s", output.Name, want, got)
			}
			golden, err := os.ReadFile(filepath.Join(dir, output.Name+".golden"))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(output.Source, golden) {
				t.Errorf("%s differs from golden\n--- want\n%s\n--- got\n%s", output.Name, golden, output.Source)
			}
			write(output.Name, output.Source)
		}
	}
	for _, args := range [][]string{{"build", "./..."}, {"test", "./..."}} {
		cmd := exec.Command("go", args...)
		cmd.Dir = buildDir
		cmd.Env = append(os.Environ(), "GOWORK=off")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go %s: %v\n%s", strings.Join(args, " "), err, output)
		}
	}
}

func TestGoSourceLibraryEmissionRejectsInvalidModes(t *testing.T) {
	shellFile := &syntax.File{}
	if _, err := lower.Compile(shellFile, lower.Options{Library: true}); err == nil || !strings.Contains(err.Error(), "requires Go source") {
		t.Fatalf("shell library emission error = %v", err)
	}
	program, err := gosource.Parse(strings.NewReader("package p\n"), "p.go", gosource.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lower.Compile(program.File, lower.Options{Package: "p", Library: true, Entry: "Run"}); err == nil || !strings.Contains(err.Error(), "cannot define a runtime entry") {
		t.Fatalf("library entry error = %v", err)
	}
	if _, err := lower.Compile(program.File, lower.Options{Library: true}); err == nil || !strings.Contains(err.Error(), "requires the source package name") {
		t.Fatalf("missing library package error = %v", err)
	}
}
