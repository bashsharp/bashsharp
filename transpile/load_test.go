package transpile

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/qiangli/bashsharp/front"

	"mvdan.cc/sh/v3/interp"
)

// Sprint 118, W1 (Bashy half): regressions for the Go-source front end as it
// is actually linked — this package imports mvdan.cc/sh/v3/gosource in the
// DEFAULT build, so everything here loads real Go source through it.

// TestGoSourcePackageFilesHonorsBuildConstraints is review finding 6. A
// package with a per-GOOS main builds natively on either host; collecting both
// files rejected it as a duplicate main.
func TestGoSourcePackageFilesHonorsBuildConstraints(t *testing.T) {
	other := "linux"
	if runtime.GOOS == "linux" {
		other = "darwin"
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "main_"+runtime.GOOS+".go"),
		"package main\n\nfunc main() {}\n")
	writeFile(t, filepath.Join(dir, "main_"+other+".go"),
		"package main\n\nfunc main() {}\n")
	writeFile(t, filepath.Join(dir, "excluded.go"),
		"//go:build ignore\n\npackage main\n\nfunc excluded() {}\n")
	writeFile(t, filepath.Join(dir, "tagged.go"),
		"//go:build never_set_by_this_build\n\npackage main\n\nfunc tagged() {}\n")
	writeFile(t, filepath.Join(dir, "helper_test.go"),
		"package main\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) {}\n")

	names, err := goSourcePackageFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, name := range names {
		got = append(got, filepath.Base(name))
	}
	want := []string{"main_" + runtime.GOOS + ".go"}
	if len(got) != 1 || got[0] != want[0] {
		t.Fatalf("selected %q, want %q", got, want)
	}

	// And the selection must be usable: a package that go/build accepts must
	// load, rather than failing as a duplicate main.
	in, err := front.CollectGoSources(front.GoSourceResolution{Enabled: true}, dir, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := front.LoadGoSource(in, front.GoSourceOptions{RunMain: true, Dir: dir}); err != nil {
		t.Fatalf("load of a constraint-selected package: %v", err)
	}
}

func TestGoSourcePackageFilesRejectsEmptyAndCgo(t *testing.T) {
	if _, err := goSourcePackageFiles(t.TempDir()); err == nil {
		t.Error("an empty directory must be refused")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "c.go"),
		"package main\n\n// #include <stdio.h>\nimport \"C\"\n\nfunc main() {}\n")
	_, err := goSourcePackageFiles(dir)
	if err == nil || !strings.Contains(err.Error(), "cgo") {
		t.Errorf("err = %v, want a cgo refusal", err)
	}
}

// TestTranspileDirectoryRecipeNeverOverwritesAnOriginal is review finding 1,
// the P0. A directory operand's members are collected inside the command, so
// checking only what was spelled on the command line let a generated file land
// on top of an upstream program.
func TestTranspileDirectoryRecipeNeverOverwritesAnOriginal(t *testing.T) {
	setup := func(t *testing.T) (dir, pkg string, sums map[string]string) {
		t.Helper()
		dir = t.TempDir()
		pkg = filepath.Join(dir, "pkg")
		writeFile(t, filepath.Join(pkg, "main.go"),
			"package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(helper()) }\n")
		writeFile(t, filepath.Join(pkg, "helper.go"),
			"package main\n\nfunc helper() string { return \"helper\" }\n")
		sums = map[string]string{}
		for _, name := range []string{"main.go", "helper.go"} {
			data, err := os.ReadFile(filepath.Join(pkg, name))
			if err != nil {
				t.Fatal(err)
			}
			sums[name] = string(data)
		}
		return dir, pkg, sums
	}
	unchanged := func(t *testing.T, pkg string, sums map[string]string) {
		t.Helper()
		for name, want := range sums {
			data, err := os.ReadFile(filepath.Join(pkg, name))
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if string(data) != want {
				t.Errorf("%s was modified:\n got %q\nwant %q", name, data, want)
			}
		}
	}

	cases := []struct {
		name string
		// args builds the argv from the scratch dir and the package dir.
		args func(dir, pkg string) []string
		skip func(t *testing.T, dir, pkg string)
	}{
		{
			name: "output over a collected original",
			args: func(dir, pkg string) []string {
				return []string{"--bashpp", "--source=go", pkg,
					"-o", filepath.Join(pkg, "helper.go"),
					"--map", filepath.Join(dir, "out.map")}
			},
		},
		{
			name: "map over a collected original",
			args: func(dir, pkg string) []string {
				return []string{"--bashpp", "--source=go", pkg,
					"-o", filepath.Join(dir, "out.go"),
					"--map", filepath.Join(pkg, "main.go")}
			},
		},
		{
			name: "output through a symlink to a collected original",
			skip: func(t *testing.T, dir, pkg string) {
				if err := os.Symlink(filepath.Join(pkg, "helper.go"),
					filepath.Join(dir, "alias.go")); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			},
			args: func(dir, pkg string) []string {
				return []string{"--bashpp", "--source=go", pkg,
					"-o", filepath.Join(dir, "alias.go"),
					"--map", filepath.Join(dir, "out.map")}
			},
		},
		{
			name: "output through a hard link to a collected original",
			skip: func(t *testing.T, dir, pkg string) {
				if err := os.Link(filepath.Join(pkg, "helper.go"),
					filepath.Join(dir, "hardlink.go")); err != nil {
					t.Skipf("hard links unavailable: %v", err)
				}
			},
			args: func(dir, pkg string) []string {
				return []string{"--bashpp", "--source=go", pkg,
					"-o", filepath.Join(dir, "hardlink.go"),
					"--map", filepath.Join(dir, "out.map")}
			},
		},
		{
			name: "the default map path over a collected original",
			args: func(dir, pkg string) []string {
				// -o pkg/helper.go with no --map: the map defaults beside the
				// output, and the OUTPUT is the original.
				return []string{"--bashpp", "--source=go", pkg,
					"-o", filepath.Join(pkg, "helper.go")}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, pkg, sums := setup(t)
			if tc.skip != nil {
				tc.skip(t, dir, pkg)
			}
			exit, stderr := captureTranspileStderr(t, tc.args(dir, pkg))
			if exit != 2 {
				t.Errorf("exit = %d, want 2 (stderr %q)", exit, stderr)
			}
			if !strings.Contains(stderr, "cannot be the same file") {
				t.Errorf("stderr = %q, want a same-file refusal", stderr)
			}
			unchanged(t, pkg, sums)
		})
	}

	// The legitimate destination still works, so the check is not a blanket
	// refusal of directory recipes.
	t.Run("a destination outside the package still transpiles", func(t *testing.T) {
		dir, pkg, sums := setup(t)
		out := filepath.Join(dir, "gen", "generated.go")
		exit, stderr := captureTranspileStderr(t,
			[]string{"--bashpp", "--source=go", pkg, "-o", out})
		if exit != 0 {
			t.Fatalf("exit = %d, want 0 (stderr %q)", exit, stderr)
		}
		if _, err := os.Stat(out); err != nil {
			t.Errorf("no artifact: %v", err)
		}
		unchanged(t, pkg, sums)
	})
}

// TestTranspileBuildOnlyNonMainPackage is review finding 7: `go build` compiles
// a non-main package, so a build-phase corpus row must be able to transpile one
// too. Asking the front end for entry calls unconditionally rejected every such
// package.
func TestTranspileBuildOnlyNonMainPackage(t *testing.T) {
	dir := t.TempDir()
	pkg := filepath.Join(dir, "pkg")
	writeFile(t, filepath.Join(pkg, "p.go"),
		"package p\n\nvar X = 7\n\nfunc F() int { return X + 1 }\n")

	out := filepath.Join(dir, "generated.go")
	mapFile := filepath.Join(dir, "generated.go.map")
	exit, stderr := captureTranspileStderr(t,
		[]string{"--bashpp", "--source=go", pkg, "-o", out, "--map", mapFile})
	if exit != 0 {
		t.Fatalf("exit = %d, want 0 (stderr %q)", exit, stderr)
	}
	art := readSourceMap(t, mapFile)
	if art.SourceKind != "go" || len(art.Sources) != 1 {
		t.Errorf("map = %+v", art)
	}

	// --check on the same package is semantic-only and must also accept it.
	in, err := front.CollectGoSources(front.GoSourceResolution{Enabled: true}, pkg, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	prog, err := front.LoadGoSource(in, front.GoSourceOptions{RunMain: false, Dir: pkg})
	if err != nil {
		t.Fatalf("--check load: %v", err)
	}
	if prog.Package != "p" || prog.Main != "" {
		t.Errorf("package = %q main = %q, want p / none", prog.Package, prog.Main)
	}

	// Running it is still refused: a non-main package has no entry point, and
	// that refusal is the front end's, in Go's own words.
	if _, err := front.LoadGoSource(in, front.GoSourceOptions{RunMain: true, Dir: pkg}); err == nil {
		t.Error("running a non-main package must be refused")
	} else if !strings.Contains(err.Error(), "package main") {
		t.Errorf("refusal = %v, want it to name package main", err)
	}
}

// TestGoSourceResolvesModuleImports is review finding 3: without the shared
// module importer, every local or helper-module import was rejected at load
// time, before lowering's own importer could resolve it. The load runs from a
// cwd that is NOT the module, matching the harness convention of an
// assets-only working directory and an absolute source path.
func TestGoSourceResolvesModuleImports(t *testing.T) {
	root := t.TempDir()
	helper := filepath.Join(root, "helper")
	app := filepath.Join(root, "app")
	writeFile(t, filepath.Join(helper, "go.mod"), "module example.com/helper\n\ngo 1.26\n")
	writeFile(t, filepath.Join(helper, "greet.go"),
		"package helper\n\nfunc Greet() string { return \"hi\" }\n")
	writeFile(t, filepath.Join(app, "go.mod"),
		"module example.com/app\n\ngo 1.26\n\nrequire example.com/helper v0.0.0\n\nreplace example.com/helper => ../helper\n")
	writeFile(t, filepath.Join(app, "main.go"),
		"package main\n\nimport (\n\t\"fmt\"\n\n\t\"example.com/helper\"\n)\n\nfunc main() { fmt.Println(helper.Greet()) }\n")

	// An assets-only cwd, deliberately outside both modules.
	elsewhere := t.TempDir()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(elsewhere); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })

	source := filepath.Join(app, "main.go")
	in, err := front.CollectGoSources(front.GoSourceResolution{Enabled: true}, source, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if in.Dir != app {
		t.Fatalf("module dir = %q, want the SOURCE's directory %q", in.Dir, app)
	}
	prog, err := front.LoadGoSource(in, front.GoSourceOptions{RunMain: true, Dir: in.Dir})
	if err != nil {
		t.Fatalf("module import was not resolved: %v", err)
	}
	if prog.Package != "main" || prog.Main != "main" {
		t.Errorf("package = %q main = %q", prog.Package, prog.Main)
	}

	// Without the importer the same load fails, so the test above is not
	// passing for some unrelated reason.
	if _, err := front.LoadGoSource(in, front.GoSourceOptions{RunMain: true, Dir: elsewhere}); err == nil {
		t.Error("control: resolving from a foreign directory unexpectedly succeeded")
	}
}

// TestGoSourceDiagnosticsNameTheOriginalFile pins what a differential harness
// compares: the front end's positioned diagnostic, naming the ORIGINAL file.
func TestGoSourceDiagnosticsNameTheOriginalFile(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		// gc's wording (the syntax verdict is gc's own parser since S154 D3).
		{"syntax", "package main\n\nif true; then echo shell-ran; fi\n", "syntax error: non-declaration statement outside function body"},
		{"type", "package main\n\nfunc main() { _ = undefinedName }\n", "undefined: undefinedName"},
		{"import", "package main\n\nimport \"example.com/absent\"\n\nfunc main() { _ = absent.X }\n", "example.com/absent"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			source := filepath.Join(dir, "original.go")
			writeFile(t, source, tc.body)
			in, err := front.CollectGoSources(front.GoSourceResolution{Enabled: true}, source, "", nil)
			if err != nil {
				t.Fatal(err)
			}
			_, err = front.LoadGoSource(in, front.GoSourceOptions{RunMain: true, Dir: in.Dir})
			if err == nil {
				t.Fatal("want a diagnostic")
			}
			if !strings.Contains(err.Error(), source) {
				t.Errorf("diagnostic %q does not name the original file %q", err, source)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("diagnostic %q, want it to mention %q", err, tc.want)
			}
			// No shell wording may appear: these bytes are never reparsed.
			for _, shellish := range []string{"command not found", "words and redirects", "syntax error near"} {
				if strings.Contains(err.Error(), shellish) {
					t.Errorf("shell diagnostic leaked: %q", err)
				}
			}
		})
	}
}

// TestGoSourceModuleDirIsWired closes the last nil seam of the story: sh
// de4ff069 landed interp.GoSourceModuleDir, and internal/agentos's init must
// bind it. A nil hook here is not a degraded mode, it is the old bug — the
// interpreter falls back to resolving imports in the runner's cwd.
func TestGoSourceModuleDirIsWired(t *testing.T) {
	if front.GoSourceModuleDir == nil {
		t.Fatal("front.GoSourceModuleDir is nil: the module context does not travel with the source")
	}
	// The option must actually apply to a runner, and must reject a
	// non-directory rather than silently accepting it.
	dir := t.TempDir()
	r, err := interp.New()
	if err != nil {
		t.Fatal(err)
	}
	if err := front.GoSourceModuleDir(dir)(r); err != nil {
		t.Fatalf("applying the module dir %q: %v", dir, err)
	}
	file := filepath.Join(dir, "notadir")
	writeFile(t, file, "x\n")
	if err := front.GoSourceModuleDir(file)(r); err == nil {
		t.Error("a file was accepted as a module context; want a refusal")
	}
}
