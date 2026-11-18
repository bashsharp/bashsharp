package lower

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func testModuleRoot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	root, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOWORK", "off")
	t.Setenv("GO111MODULE", "on")
	t.Setenv("GOPROXY", "off")
	t.Setenv("GOSUMDB", "off")
	t.Setenv("GOFLAGS", "")
	return root
}

func testModuleWrite(t *testing.T, root, name, body string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func testModuleImport(t *testing.T, i types.Importer, path string) *types.Package {
	t.Helper()
	p, err := i.Import(path)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestModuleImporterSourceOnlyContext(t *testing.T) {
	root := testModuleRoot(t)
	testModuleWrite(t, root, "go.mod", "module example.test/app\n\ngo 1.25\n")
	testModuleWrite(t, root, "main.bpp", "import \"example.test/app/internal/value\"\n")
	testModuleWrite(t, root, "internal/value/v.go", "package value\nconst N = 7\n")

	importer := newModuleImporter(root)
	pkg, err := importer.Import("example.test/app/internal/value")
	if err != nil {
		t.Fatalf("valid source-only module internal import rejected: %v", err)
	}
	if pkg.Path() != "example.test/app/internal/value" {
		t.Fatalf("expected example.test/app/internal/value, got %q", pkg.Path())
	}
}

func TestModuleImporterSourceOnlyWorkspaceContext(t *testing.T) {
	root := testModuleRoot(t)
	t.Setenv("GOWORK", filepath.Join(root, "go.work"))

	testModuleWrite(t, root, "go.work", "go 1.25\nuse (\n ./a\n ./b\n)\n")
	testModuleWrite(t, root, "a/go.mod", "module example.test/a\n\ngo 1.25\n")
	testModuleWrite(t, root, "b/go.mod", "module example.test/b\n\ngo 1.25\n")
	testModuleWrite(t, root, "a/main.bpp", "import \"example.test/a/internal/value\"\n")
	testModuleWrite(t, root, "a/internal/value/v.go", "package value\nconst N = 7\n")

	dirA := filepath.Join(root, "a")
	importer := newModuleImporter(dirA)

	// Import stdlib first to verify caller identity remains pure/fixed
	_, err := importer.Import("fmt")
	if err != nil {
		t.Fatalf("stdlib import failed: %v", err)
	}

	pkg, err := importer.Import("example.test/a/internal/value")
	if err != nil {
		t.Fatalf("valid source-only workspace internal import rejected: %v", err)
	}
	if pkg.Path() != "example.test/a/internal/value" {
		t.Fatalf("expected example.test/a/internal/value, got %q", pkg.Path())
	}
}

func TestModuleImporterCallerIdentityFixed(t *testing.T) {
	root := testModuleRoot(t)
	testModuleWrite(t, root, "go.mod", "module example.test/app\n\ngo 1.25\n")
	testModuleWrite(t, root, "main.bpp", "import \"fmt\"\nimport \"example.test/app/internal/value\"\n")
	testModuleWrite(t, root, "internal/value/v.go", "package value\nconst N = 7\n")

	importer := newModuleImporter(root)
	_, err := importer.Import("fmt")
	if err != nil {
		t.Fatalf("stdlib import failed: %v", err)
	}

	pkg, err := importer.Import("example.test/app/internal/value")
	if err != nil {
		t.Fatalf("internal import rejected after stdlib import: %v", err)
	}
	if pkg.Path() != "example.test/app/internal/value" {
		t.Fatalf("expected example.test/app/internal/value, got %q", pkg.Path())
	}
}

func TestModuleImporterIdentityAndNoInit(t *testing.T) {
	root := testModuleRoot(t)
	testModuleWrite(t, root, "go.mod", "module example.test/app\n\ngo 1.25\n")
	testModuleWrite(t, root, "provider/p.go", `package actualname
import "time"
func init(){panic("IMPORT MUST NOT EXECUTE INIT")}
type T struct{ When time.Time }
`)
	testModuleWrite(t, root, "consumer/p.go", `package consumer
import("example.test/app/provider";"time")
func Value() actualname.T {return actualname.T{}}
func When() time.Time {return time.Time{}}
`)
	importer := newModuleImporter(root)
	consumer := testModuleImport(t, importer, "example.test/app/consumer")
	provider := testModuleImport(t, importer, "example.test/app/provider")
	stdlib := testModuleImport(t, importer, "time")
	if provider.Name() != "actualname" {
		t.Fatal(provider.Name())
	}
	for name, want := range map[string]types.Type{"Value": provider.Scope().Lookup("T").Type(), "When": stdlib.Scope().Lookup("Time").Type()} {
		got := consumer.Scope().Lookup(name).Type().(*types.Signature).Results().At(0).Type()
		if !types.Identical(got, want) {
			t.Fatalf("identity %s: %v / %v", name, got, want)
		}
	}
	if again := testModuleImport(t, importer, "example.test/app/provider"); again != provider {
		t.Fatal("package identity not reused")
	}
}

func TestModuleImporterContextMatrix(t *testing.T) {
	t.Run("workspace-and-replace", func(t *testing.T) {
		root := testModuleRoot(t)
		testModuleWrite(t, root, "go.work", "go 1.25\nuse (\n ./app\n ./used\n)\nreplace example.test/replaced => ./replacement\n")
		testModuleWrite(t, root, "app/go.mod", "module example.test/app\ngo 1.25\nrequire example.test/replaced v0.0.0\n")
		testModuleWrite(t, root, "used/go.mod", "module example.test/used\ngo 1.25\n")
		testModuleWrite(t, root, "used/pkg/p.go", "package usedpkg\ntype T int\n")
		testModuleWrite(t, root, "replacement/go.mod", "module example.test/replaced\ngo 1.25\n")
		testModuleWrite(t, root, "replacement/pkg/p.go", "package replacedpkg\ntype T int\n")
		t.Setenv("GOWORK", filepath.Join(root, "go.work"))
		i := newModuleImporter(filepath.Join(root, "app"))
		for path, name := range map[string]string{"example.test/used/pkg": "usedpkg", "example.test/replaced/pkg": "replacedpkg"} {
			if p := testModuleImport(t, i, path); p.Name() != name {
				t.Fatal(p)
			}
		}
	})
	t.Run("module-vendor", func(t *testing.T) {
		root := testModuleRoot(t)
		testModuleWrite(t, root, "go.mod", "module example.test/app\ngo 1.25\nrequire example.test/dep v1.0.0\n")
		testModuleWrite(t, root, "vendor/modules.txt", "# example.test/dep v1.0.0\n## explicit; go 1.25\nexample.test/dep/pkg\n")
		testModuleWrite(t, root, "vendor/example.test/dep/pkg/p.go", "package vendored\ntype T int\n")
		t.Setenv("GOFLAGS", "-mod=vendor")
		testModuleImport(t, newModuleImporter(root), "example.test/dep/pkg")
	})
	t.Run("gopath", func(t *testing.T) {
		root := testModuleRoot(t)
		testModuleWrite(t, root, "src/app/app.go", "package app\n")
		testModuleWrite(t, root, "src/example.test/dep/pkg/p.go", "package legacy\ntype T int\n")
		t.Setenv("GO111MODULE", "off")
		t.Setenv("GOPATH", root)
		testModuleImport(t, newModuleImporter(filepath.Join(root, "src/app")), "example.test/dep/pkg")
	})
	t.Run("gopath-vendor", func(t *testing.T) {
		root := testModuleRoot(t)
		testModuleWrite(t, root, "src/app/app.go", "package app\n")
		testModuleWrite(t, root, "src/app/vendor/example.test/dep/pkg/p.go", "package legacyvendor\ntype T int\n")
		testModuleWrite(t, root, "src/app/app.go", "package app\nimport \"example.test/dep/pkg\"\nvar Value legacyvendor.T\n")
		t.Setenv("GO111MODULE", "off")
		t.Setenv("GOPATH", root)
		command := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", ".")
		command.Dir = filepath.Join(root, "src/app")
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("native GOPATH vendor oracle: %s/%v", output, err)
		}
		testModuleImport(t, newModuleImporter(filepath.Join(root, "src/app")), "example.test/dep/pkg")
	})
}

func TestModuleImporterInternalVisibility(t *testing.T) {
	root := testModuleRoot(t)
	testModuleWrite(t, root, "dep/go.mod", "module example.test/dep\ngo 1.25\n")
	testModuleWrite(t, root, "dep/internal/secret/p.go", "package secret\nconst Value = 7\n")
	testModuleWrite(t, root, "app/go.mod", "module example.test/app\ngo 1.25\nrequire example.test/dep v0.0.0\nreplace example.test/dep => ../dep\n")
	source := "package app\nimport \"example.test/dep/internal/secret\"\nvar Value = secret.Value\n"
	testModuleWrite(t, root, "app/app.go", source)
	app := filepath.Join(root, "app")
	command := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", ".")
	command.Dir = app
	output, buildErr := command.CombinedOutput()
	if buildErr == nil || !strings.Contains(string(output), "internal package") {
		t.Fatalf("invalid native oracle %s/%v", output, buildErr)
	}
	fs := token.NewFileSet()
	file, err := parser.ParseFile(fs, filepath.Join(app, "app.go"), source, 0)
	if err != nil {
		t.Fatal(err)
	}
	config := types.Config{Importer: newModuleImporter(app)}
	_, err = config.Check("example.test/app", fs, []*ast.File{file}, nil)
	if err == nil {
		t.Fatal("importer accepts external internal package rejected by native Go")
	}
}

func TestModuleImporterNestedInternal(t *testing.T) {
	root := testModuleRoot(t)
	testModuleWrite(t, root, "dep/go.mod", "module example.test/dep\ngo 1.25\n")
	testModuleWrite(t, root, "dep/internal/nested/internal/secret/p.go", "package secret\nconst Value = 42\n")
	testModuleWrite(t, root, "dep/client/client.go", "package client\nimport \"example.test/dep/internal/nested/internal/secret\"\nvar Value = secret.Value\n")

	clientDir := filepath.Join(root, "dep/client")
	command := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", ".")
	command.Dir = clientDir
	output, buildErr := command.CombinedOutput()
	if buildErr == nil || !strings.Contains(string(output), "internal package") {
		t.Fatalf("native oracle expected failure for nested internal package: %s/%v", output, buildErr)
	}

	fs := token.NewFileSet()
	file, err := parser.ParseFile(fs, filepath.Join(clientDir, "client.go"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}

	config := types.Config{Importer: newModuleImporter(clientDir)}
	_, err = config.Check("example.test/dep/client", fs, []*ast.File{file}, nil)
	if err == nil {
		t.Fatal("importer accepts nested internal package that should be rejected by LAST internal rule")
	}
}

func TestModuleImporterStdlibInternalGOPATH(t *testing.T) {
	root := testModuleRoot(t)
	testModuleWrite(t, root, "src/app/app.go", "package app\nimport \"internal/abi\"\nvar _ = abi.Int\n")
	t.Setenv("GO111MODULE", "off")
	t.Setenv("GOPATH", root)

	appDir := filepath.Join(root, "src/app")
	command := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", ".")
	command.Dir = appDir
	output, buildErr := command.CombinedOutput()
	if buildErr == nil || !strings.Contains(string(output), "internal package") {
		t.Fatalf("native oracle expected failure for GOPATH app importing internal/abi: %s/%v", output, buildErr)
	}

	fs := token.NewFileSet()
	file, err := parser.ParseFile(fs, filepath.Join(appDir, "app.go"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}

	config := types.Config{Importer: newModuleImporter(appDir)}
	_, err = config.Check("app", fs, []*ast.File{file}, nil)
	if err == nil {
		t.Fatal("importer accepts stdlib internal/abi from GOPATH app package")
	}
}

func TestModuleImporterSourceOnlyGOPATHVendor(t *testing.T) {
	root := testModuleRoot(t)
	testModuleWrite(t, root, "src/app/main.bpp", "import \"example.test/dep/pkg\"\n")
	testModuleWrite(t, root, "src/app/vendor/example.test/dep/pkg/p.go", "package vendored\ntype T int\n")
	testModuleWrite(t, root, "src/example.test/dep/pkg/p.go", "package wrongglobal\ntype T string\n")
	testModuleWrite(t, root, "src/app/vendor/example.test/consumer/p.go", "package consumer\nimport \"example.test/dep/pkg\"\nfunc Value() vendored.T { return 7 }\n")
	t.Setenv("GO111MODULE", "off")
	t.Setenv("GOPATH", root)
	app := filepath.Join(root, "src/app")
	i := newModuleImporter(app)
	consumer := testModuleImport(t, i, "example.test/consumer")
	pkg := testModuleImport(t, i, "example.test/dep/pkg")
	got := consumer.Scope().Lookup("Value").Type().(*types.Signature).Results().At(0).Type()
	if !types.Identical(got, pkg.Scope().Lookup("T").Type()) {
		t.Fatalf("vendored type identity lost: %v / %v", got, pkg)
	}
	if pkg.Name() != "vendored" {
		t.Fatal(pkg)
	}
	entries, err := os.ReadDir(app)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".go") {
			t.Fatalf("import created Go source %s", entry.Name())
		}
	}
}
