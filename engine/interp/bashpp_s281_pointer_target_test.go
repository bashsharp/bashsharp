//go:build full

package interp_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/lower"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestS281ImportedPointerTargets(t *testing.T) {
	dep := `package dep

type Type struct{ Name string }

const (
	TINVALID = 0
	TBOOL    = 1
	TINT     = 2
)

var Types [4]*Type

var Config = struct {
	GOOS  string
	Count int
}{GOOS: "plan9", Count: 1}

func init() {
	Types[TBOOL] = &Type{Name: "bool"}
	Types[TINT] = &Type{Name: "int"}
}`
	main := `package main

import (
	"fmt"

	"example.com/pointertarget/dep"
)

var basics = [...]**dep.Type{
	dep.TINVALID: new(*dep.Type),
	dep.TBOOL:    &dep.Types[dep.TBOOL],
	dep.TINT:     &dep.Types[dep.TINT],
}

func main() {
	fmt.Println((*basics[dep.TBOOL]).Name, (*basics[dep.TINT]).Name, len(basics))
	fmt.Println(*basics[dep.TINVALID] == nil)
	for k := dep.TBOOL; k <= dep.TINT; k++ {
		fmt.Println(k, (*basics[k]).Name)
	}

	goos := &dep.Config.GOOS
	count := &dep.Config.Count
	fmt.Println(*goos, *count)
	*goos = "aix"
	*count = 7
	fmt.Println(dep.Config.GOOS, dep.Config.Count)

	*basics[dep.TBOOL] = &dep.Type{Name: "rebound"}
	fmt.Println(dep.Types[dep.TBOOL].Name)

	whole := &dep.Types
	fmt.Println(len(*whole), (*whole)[dep.TINT].Name)
}`
	differGoSourceDependencyModule(t, "example.com/pointertarget", dep, main)
}

func differGoSourceDependencyModule(t *testing.T, module, dependency, source string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "dep"), 0700); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"go.mod":     "module " + module + "\n\ngo 1.27\n",
		"dep/dep.go": dependency,
		"main.go":    source,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(dir, "main.go")
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	binary := filepath.Join(t.TempDir(), "oracle")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-p", "2", "-o", binary, ".")
	build.Dir = dir
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("oracle build: %v %s", err, out)
	}
	oracle := exec.CommandContext(ctx, binary)
	oracle.Dir = dir
	want, err := oracle.CombinedOutput()
	if err != nil {
		t.Fatalf("oracle: %v %s", err, want)
	}

	program, err := gosource.Parse(strings.NewReader(source), path, gosource.Options{RunMain: true, Importer: lower.NewModuleImporter(dir)})
	if err != nil {
		t.Fatalf("gosource.Parse: %v", err)
	}
	var stdout, stderr bytes.Buffer
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(dir), interp.GoSourceModuleDir(dir), interp.StdIO(nil, &stdout, &stderr))
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(ctx, program.File); err != nil {
		t.Fatalf("Runner: %v; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	if stdout.String() != string(want) || stderr.Len() != 0 {
		t.Fatalf("Runner %q/%q; native Go %q", stdout.String(), stderr.String(), want)
	}
}
