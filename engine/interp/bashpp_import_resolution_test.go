//go:build full

package interp

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestBashPPResolveLocalModuleAllImportForms(t *testing.T) {
	root := t.TempDir()
	writeImportFixture(t, root, "go.mod", "module example.com/app\n\ngo 1.25\n")
	writeImportFixture(t, root, "lib/lib.go", "package actualname\n\nimport \"fmt\"\n\nfunc Print(v string) { fmt.Println(v) }\n")
	req := nativeResolveRequest(root, "GOWORK=off", "GO111MODULE=on")
	if got, err := (nativeBashPPEvaluator{}).Resolve(context.Background(), req, "example.com/app/lib"); err != nil || got != "actualname" {
		t.Fatalf("Resolve = %q, %v", got, err)
	}

	// Exercise ordinary, aliased, blank, and dot bindings transactionally on
	// independent sessions; Go itself rejects importing one path twice in a
	// single compilation unit, and the namespace deliberately does the same.
	for _, form := range []string{"", "alias ", "_ ", ". "} {
		eval := &recordingBashPPEval{resolved: map[string]string{"example.com/app/lib": "actualname"}}
		r := newInjectedBashPPRunner(t, eval)
		r.Dir = root
		src := "import " + form + "\"example.com/app/lib\"\n"
		if err := r.Run(context.Background(), parseBashPPInternal(t, src)); err != nil {
			t.Fatalf("%q: %v", src, err)
		}
		if len(r.bashPPImports) != 1 {
			t.Fatalf("%q: imports %#v", src, r.bashPPImports)
		}
	}
}

func TestBashPPLocalModuleParseWalkPrintInterp(t *testing.T) {
	root := t.TempDir()
	writeImportFixture(t, root, "go.mod", "module example.com/app\n\ngo 1.25\n")
	writeImportFixture(t, root, "ordinary/pkg.go", "package ordinary\nimport \"fmt\"\nfunc Print(string) { fmt.Println(\"ordinary\") }\n")
	writeImportFixture(t, root, "aliased/pkg.go", "package original\nimport \"fmt\"\nfunc Print(string) { fmt.Println(\"alias\") }\n")
	writeImportFixture(t, root, "dotted/pkg.go", "package dotted\nimport \"fmt\"\nfunc Dot(string) { fmt.Println(\"dot\") }\n")
	writeImportFixture(t, root, "blank/pkg.go", "package blank\n")
	src := "import (\n\t\"example.com/app/ordinary\"\n\ta \"example.com/app/aliased\"\n\t_ \"example.com/app/blank\"\n\t. \"example.com/app/dotted\"\n)\nordinary.Print(\"x\")\na.Print(\"x\")\nDot(\"x\")\n"
	f := parseBashPPInternal(t, src)
	imports, calls := 0, 0
	syntax.Walk(f, func(node syntax.Node) bool {
		switch node.(type) {
		case *syntax.BashPPImportSpec:
			imports++
		case *syntax.BashPPCall:
			calls++
		}
		return true
	})
	if imports != 4 || calls != 3 {
		t.Fatalf("walk imports=%d calls=%d", imports, calls)
	}
	var printed bytes.Buffer
	if err := syntax.NewPrinter().Print(&printed, f); err != nil {
		t.Fatal(err)
	}
	if reparsed := parseBashPPInternal(t, printed.String()); len(reparsed.Stmts) != len(f.Stmts) {
		t.Fatalf("print/reparse statements = %d, want %d", len(reparsed.Stmts), len(f.Stmts))
	}
	var out bytes.Buffer
	r, err := New(Dir(root), StdIO(nil, &out, &out), Lang(syntax.LangBashPP))
	if err != nil {
		t.Fatal(err)
	}
	r.bashPPTools = bashPPToolchain{goBinary: filepath.Join(runtime.GOROOT(), "bin", "go"), eval: nativeBashPPEvaluator{}}
	if err := r.Run(context.Background(), f); err != nil {
		t.Fatalf("run: %v\n%s", err, out.String())
	}
	if got := out.String(); got != "ordinary\nalias\ndot\n" {
		t.Fatalf("output %q", got)
	}
}

func TestBashPPResolveWorkspaceUseAndReplace(t *testing.T) {
	root := t.TempDir()
	writeImportFixture(t, root, "go.work", "go 1.25\n\nuse (\n ./app\n ./used\n)\n\nreplace example.com/replaced => ./replacement\n")
	writeImportFixture(t, root, "app/go.mod", "module example.com/app\n\ngo 1.25\n\nrequire example.com/replaced v0.0.0\n")
	writeImportFixture(t, root, "used/go.mod", "module example.com/used\n\ngo 1.25\n")
	writeImportFixture(t, root, "used/pkg/pkg.go", "package usedpkg\n")
	writeImportFixture(t, root, "replacement/go.mod", "module example.com/replaced\n\ngo 1.25\n")
	writeImportFixture(t, root, "replacement/pkg/pkg.go", "package replacedpkg\n")
	req := nativeResolveRequest(filepath.Join(root, "app"), "GOWORK="+filepath.Join(root, "go.work"), "GO111MODULE=on")
	for importPath, want := range map[string]string{
		"example.com/used/pkg": "usedpkg", "example.com/replaced/pkg": "replacedpkg",
	} {
		if got, err := (nativeBashPPEvaluator{}).Resolve(context.Background(), req, importPath); err != nil || got != want {
			t.Errorf("Resolve(%q) = %q, %v", importPath, got, err)
		}
	}
}

func TestBashPPResolveVendorAndGOPATH(t *testing.T) {
	t.Run("vendor", func(t *testing.T) {
		root := t.TempDir()
		writeImportFixture(t, root, "go.mod", "module example.com/app\n\ngo 1.25\n\nrequire example.com/dep v1.0.0\n")
		writeImportFixture(t, root, "vendor/modules.txt", "# example.com/dep v1.0.0\n## explicit; go 1.25\nexample.com/dep/pkg\n")
		writeImportFixture(t, root, "vendor/example.com/dep/pkg/pkg.go", "package vendored\n")
		req := nativeResolveRequest(root, "GOWORK=off", "GO111MODULE=on", "GOFLAGS=-mod=vendor")
		if got, err := (nativeBashPPEvaluator{}).Resolve(context.Background(), req, "example.com/dep/pkg"); err != nil || got != "vendored" {
			t.Fatalf("Resolve = %q, %v", got, err)
		}
	})
	t.Run("gopath", func(t *testing.T) {
		root := t.TempDir()
		app := filepath.Join(root, "src", "app")
		writeImportFixture(t, root, "src/example.com/dep/pkg/pkg.go", "package legacy\n")
		if err := os.MkdirAll(app, 0o755); err != nil {
			t.Fatal(err)
		}
		req := nativeResolveRequest(app, "GOWORK=off", "GO111MODULE=off", "GOPATH="+root)
		if got, err := (nativeBashPPEvaluator{}).Resolve(context.Background(), req, "example.com/dep/pkg"); err != nil || got != "legacy" {
			t.Fatalf("Resolve = %q, %v", got, err)
		}
	})
}

func TestBashPPWorkspaceVendorAndGOPATHExecuteThroughShellRunner(t *testing.T) {
	if testing.Short() {
		t.Skip("builds fixture packages with the reviewed toolchain")
	}
	run := func(t *testing.T, dir, src string, env ...string) string {
		t.Helper()
		base := os.Environ()
		for _, entry := range env {
			name, value, _ := strings.Cut(entry, "=")
			base = setEnvString(base, name, value)
		}
		var out, stderr bytes.Buffer
		r, err := New(Dir(dir), StdIO(nil, &out, &stderr), Env(expand.ListEnviron(base...)), Lang(syntax.LangBashPP))
		if err != nil {
			t.Fatal(err)
		}
		if err := r.Run(context.Background(), parseBashPPInternal(t, src)); err != nil {
			t.Fatalf("run: %v: %s", err, stderr.String())
		}
		return out.String()
	}

	t.Run("workspace use and replace", func(t *testing.T) {
		root := t.TempDir()
		work := filepath.Join(root, "go.work")
		writeImportFixture(t, root, "go.work", "go 1.25\n\nuse (\n ./app\n ./used\n)\n\nreplace example.com/replaced => ./replacement\n")
		writeImportFixture(t, root, "app/go.mod", "module example.com/app\n\ngo 1.25\n\nrequire example.com/replaced v0.0.0\n")
		writeImportFixture(t, root, "used/go.mod", "module example.com/used\n\ngo 1.25\n")
		writeImportFixture(t, root, "used/pkg/pkg.go", "package usedpkg\nimport \"fmt\"\nfunc Print() { fmt.Println(\"used\") }\n")
		writeImportFixture(t, root, "replacement/go.mod", "module example.com/replaced\n\ngo 1.25\n")
		writeImportFixture(t, root, "replacement/pkg/pkg.go", "package replacedpkg\nimport \"fmt\"\nfunc Print() { fmt.Println(\"replaced\") }\n")
		src := "import (\n u \"example.com/used/pkg\"\n r \"example.com/replaced/pkg\"\n)\nu.Print()\nr.Print()\n"
		if got := run(t, filepath.Join(root, "app"), src, "GOWORK="+work, "GO111MODULE=on"); got != "used\nreplaced\n" {
			t.Fatalf("output %q", got)
		}
	})

	t.Run("vendor", func(t *testing.T) {
		root := t.TempDir()
		writeImportFixture(t, root, "go.mod", "module example.com/app\n\ngo 1.25\n\nrequire example.com/dep v1.0.0\n")
		writeImportFixture(t, root, "vendor/modules.txt", "# example.com/dep v1.0.0\n## explicit; go 1.25\nexample.com/dep/pkg\n")
		writeImportFixture(t, root, "vendor/example.com/dep/pkg/pkg.go", "package vendored\nimport \"fmt\"\nfunc Print() { fmt.Println(\"vendor\") }\n")
		if got := run(t, root, "import \"example.com/dep/pkg\"\nvendored.Print()\n", "GOWORK=off", "GO111MODULE=on", "GOFLAGS=-mod=vendor"); got != "vendor\n" {
			t.Fatalf("output %q", got)
		}
	})

	t.Run("gopath", func(t *testing.T) {
		root := t.TempDir()
		app := filepath.Join(root, "src", "app")
		writeImportFixture(t, root, "src/example.com/dep/pkg/pkg.go", "package legacy\nimport \"fmt\"\nfunc Print() { fmt.Println(\"gopath\") }\n")
		if err := os.MkdirAll(app, 0o755); err != nil {
			t.Fatal(err)
		}
		if got := run(t, app, "import \"example.com/dep/pkg\"\nlegacy.Print()\n", "GOWORK=off", "GO111MODULE=off", "GOPATH="+root); got != "gopath\n" {
			t.Fatalf("output %q", got)
		}
	})
}

func TestBashPPResolveVisibilityAndTraversalErrors(t *testing.T) {
	root := t.TempDir()
	writeImportFixture(t, root, "dep/go.mod", "module example.com/dep\n\ngo 1.25\n")
	writeImportFixture(t, root, "dep/internal/secret/secret.go", "package secret\n")
	writeImportFixture(t, root, "app/go.mod", "module example.com/app\n\ngo 1.25\n\nrequire example.com/dep v0.0.0\nreplace example.com/dep => ../dep\n")
	req := nativeResolveRequest(filepath.Join(root, "app"), "GOWORK=off", "GO111MODULE=on")
	_, err := (nativeBashPPEvaluator{}).Resolve(context.Background(), req, "example.com/dep/internal/secret")
	if err == nil || !strings.Contains(err.Error(), "internal package outside allowed tree") {
		t.Fatalf("internal error = %v", err)
	}
	for importPath, match := range map[string]string{
		"../dep/pkg": "path traversal", "example.com/dep/vendor/x": "canonical path",
	} {
		err := validateBashPPImportVisibility(req.Dir, filepath.Join(root, "dep"), importPath)
		if err == nil || !strings.Contains(err.Error(), match) {
			t.Errorf("%q error = %v", importPath, err)
		}
	}
	vendorDir := filepath.Join(root, "dep", "vendor", "example.com", "x")
	if err := os.MkdirAll(vendorDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := validateBashPPImportVisibility(req.Dir, vendorDir, "example.com/x"); err == nil || !strings.Contains(err.Error(), "vendor package outside allowed tree") {
		t.Fatalf("vendor visibility error = %v", err)
	}
	stdlibReq := nativeResolveRequest(req.Dir, "GOWORK=off", "GO111MODULE=on")
	if _, err := (nativeBashPPEvaluator{}).Resolve(context.Background(), stdlibReq, "cmd/go"); err == nil || !strings.Contains(err.Error(), "reviewed Go standard library") {
		t.Fatalf("stdlib allowlist error = %v", err)
	}
}

func TestBashPPConcurrentSessionImportsDoNotLeak(t *testing.T) {
	const sessions = 16
	var wg sync.WaitGroup
	for i := 0; i < sessions; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			path := "example.com/session/" + string(rune('a'+i))
			eval := &recordingBashPPEval{resolved: map[string]string{path: "pkg"}}
			r := newInjectedBashPPRunner(t, eval)
			if err := r.Run(context.Background(), parseBashPPInternal(t, "import \""+path+"\"\n")); err != nil {
				t.Error(err)
			}
			if len(r.bashPPImports) != 1 || r.bashPPImports["pkg"] != path {
				t.Errorf("session %d imports %#v", i, r.bashPPImports)
			}
		}(i)
	}
	wg.Wait()
}
