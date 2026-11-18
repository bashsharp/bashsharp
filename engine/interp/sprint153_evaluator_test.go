//go:build full

package interp_test

// Sprint: #153; Story: S153.4; Story-ID: e58cccba74f8
//
// Interpreted evaluator semantics. Every program under
// testdata/sprint153/<mechanism>/ is an unchanged Go program that is run
// natively and through the interpreter; stdout, stderr and the exit status
// must agree exactly. Each mechanism directory holds the reproducer that
// used to diverge next to a positive control the interpreter already
// handled, so a regression in either direction is visible. A program is
// either one *.go file or a module directory (go.mod, main.go, and one
// subdirectory per explicit package) for mechanisms that need more than one
// package.
import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/mod/modfile"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestSprint153Evaluator(t *testing.T) {
	root := filepath.Join("testdata", "sprint153")
	mechanisms, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, mechanism := range mechanisms {
		if !mechanism.IsDir() || mechanism.Name() == "evaluator" {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(root, mechanism.Name()))
		if err != nil {
			t.Fatal(err)
		}
		programs, other := 0, 0
		for _, entry := range entries {
			path := filepath.Join(root, mechanism.Name(), entry.Name())
			name := mechanism.Name() + "/" + strings.TrimSuffix(entry.Name(), ".go")
			switch {
			case entry.IsDir():
				if _, err := os.Stat(filepath.Join(path, "go.mod")); err != nil {
					// Another lane's layout (scaled controls, triage
					// cases), driven by its own test.
					other++
					continue
				}
				programs++
				t.Run(name, func(t *testing.T) {
					t.Parallel()
					differGoSourceModule(t, path)
				})
			case strings.HasSuffix(entry.Name(), ".md"), strings.HasSuffix(entry.Name(), ".txt"):
				// Findings and spike notes document a lane rather than a
				// mechanism; a .go.txt program is a refusal or negative case
				// driven by its own test, not a native-vs-interpreter differ.
				other++
			case strings.HasSuffix(entry.Name(), ".go"):
				source, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				programs++
				t.Run(name, func(t *testing.T) {
					t.Parallel()
					differGoSource(t, string(source), nil, "")
				})
			}
		}
		if programs == 0 && other < len(entries) {
			// A mechanism directory may hold only prose or another lane's
			// layout (bridge, triage, output notes; module subdirectories
			// driven by their own tests). Those are documentation, not an
			// empty mechanism; only a directory with unrecognised entries
			// and no programs is flagged.
			t.Fatalf("%s: no programs", mechanism.Name())
		}
	}
}

// differGoSourceModule runs a module directory's main package both ways: a
// native build of a copy of the module, and the interpreter loading main.go
// with every package subdirectory linked as an explicit package.
func differGoSourceModule(t *testing.T, dir string) {
	t.Helper()
	modData, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	module := modfile.ModulePath(modData)
	if module == "" {
		t.Fatalf("%s: go.mod names no module", dir)
	}
	mainSource, err := os.ReadFile(filepath.Join(dir, "main.go"))
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var packages []gosource.PackageSpec
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		files, err := filepath.Glob(filepath.Join(dir, entry.Name(), "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		spec := gosource.PackageSpec{Path: module + "/" + entry.Name()}
		for _, file := range files {
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			spec.Sources = append(spec.Sources, gosource.Source{Name: file, Data: data})
		}
		packages = append(packages, spec)
	}

	// Native oracle: build the module from a copy so no artefact lands in
	// testdata.
	scratch := t.TempDir()
	if out, err := exec.Command("cp", "-R", dir+"/.", scratch).CombinedOutput(); err != nil {
		t.Fatalf("copy module: %v %s", err, out)
	}
	binary := filepath.Join(t.TempDir(), "oracle")
	build := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-p", "2", "-o", binary, ".")
	build.Dir = scratch
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("oracle build: %v %s", err, out)
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.Command(binary)
	cmd.Dir = scratch
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	want := goSourceOutcome{stdout: stdout.String(), stderr: stderr.String()}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		want.status = exit.ExitCode()
	} else if err != nil {
		t.Fatalf("oracle run: %v", err)
	}

	program, err := gosource.Load([]gosource.Source{{Name: filepath.Join(dir, "main.go"), Data: mainSource}},
		gosource.Options{RunMain: true, Packages: packages})
	if err != nil {
		t.Fatalf("gosource.Load: %v", err)
	}
	stdout.Reset()
	stderr.Reset()
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(scratch),
		interp.StdIO(strings.NewReader(""), &stdout, &stderr), interp.Params("--"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	err = runner.Run(ctx, program.File)
	got := goSourceOutcome{stdout: stdout.String(), stderr: stderr.String()}
	var status interp.ExitStatus
	if errors.As(err, &status) {
		got.status = int(status)
	} else if err != nil {
		t.Fatalf("Runner: %v; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	if got != want {
		t.Fatalf("Runner %+v; native Go %+v", got, want)
	}
}
