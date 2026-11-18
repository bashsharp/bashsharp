package lower_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/lower"
)

func TestS243UnchangedIssue43164(t *testing.T) {
	dir := filepath.Join(runtime.GOROOT(), "test", "fixedbugs", "issue43164.dir")
	var sources []gosource.Source
	for _, name := range []string{"a.go", "b.go"} {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		sources = append(sources, gosource.Source{Name: path, Data: data})
	}
	program, err := gosource.Load(sources, gosource.Options{PreserveNativeInit: true})
	if err != nil {
		t.Fatal(err)
	}
	result, err := lower.Compile(program.File, lower.Options{Package: program.Package, Library: true, Importer: program.Importer})
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "go.mod"), []byte("module example.com/issue43164\n\ngo 1.27\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, file := range result.Files {
		if err := os.WriteFile(filepath.Join(work, filepath.Base(file.Name)), file.Source, 0600); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", ".")
	cmd.Dir = work
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("native compiledir: %v\n%s", err, out)
	}
}
