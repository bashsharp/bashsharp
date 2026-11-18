//go:build full

package lower_test

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

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/lower"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// These sources and package bodies preserve the executable public contracts in
// interp/TestBashPPLocalModuleParseWalkPrintInterp and
// interp/TestBashPPWorkspaceVendorAndGOPATHExecuteThroughShellRunner. The added
// GOPATH-vendor case applies that same public package call to a source-only
// importing directory. Import resolution must not require a sibling Go file.
func TestModuleContextAcceptance(t *testing.T) {
	// The source oracle authenticates the reviewed Go 1.27 toolchain. Keep
	// ordinary Go 1.26 library tests usable without weakening that policy.
	if runtime.Version() != "go1.27.1" {
		t.Skip("requires tests built with the reviewed Go 1.27.0 toolchain for the source interpreter oracle")
	}

	write := func(t *testing.T, root, name, body string) {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, kind := range []string{"local-module", "workspace-replace", "module-vendor", "gopath", "gopath-vendor"} {
		t.Run(kind, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("GOWORK", "off")
			t.Setenv("GO111MODULE", "on")
			t.Setenv("GOPROXY", "off")
			t.Setenv("GOSUMDB", "off")
			t.Setenv("GOFLAGS", "")
			t.Setenv("GOTOOLCHAIN", "local")
			t.Setenv("PATH", filepath.Join(runtime.GOROOT(), "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
			dir, source, want := root, "", ""
			switch kind {
			case "local-module":
				write(t, root, "go.mod", "module example.com/app\n\ngo 1.25\n")
				write(t, root, "ordinary/pkg.go", "package ordinary\nimport \"fmt\"\nfunc Print(string) { fmt.Println(\"ordinary\") }\n")
				write(t, root, "aliased/pkg.go", "package original\nimport \"fmt\"\nfunc Print(string) { fmt.Println(\"alias\") }\n")
				write(t, root, "dotted/pkg.go", "package dotted\nimport \"fmt\"\nfunc Dot(string) { fmt.Println(\"dot\") }\n")
				write(t, root, "blank/pkg.go", "package blank\n")
				source = "import (\n\t\"example.com/app/ordinary\"\n\ta \"example.com/app/aliased\"\n\t_ \"example.com/app/blank\"\n\t. \"example.com/app/dotted\"\n)\nordinary.Print(\"x\")\na.Print(\"x\")\nDot(\"x\")\n"
				want = "ordinary\nalias\ndot\n"
			case "workspace-replace":
				write(t, root, "go.work", "go 1.25\n\nuse (\n ./app\n ./used\n)\n\nreplace example.com/replaced => ./replacement\n")
				write(t, root, "app/go.mod", "module example.com/app\n\ngo 1.25\n\nrequire example.com/replaced v0.0.0\n")
				write(t, root, "used/go.mod", "module example.com/used\n\ngo 1.25\n")
				write(t, root, "used/pkg/pkg.go", "package usedpkg\nimport \"fmt\"\nfunc Print() { fmt.Println(\"used\") }\n")
				write(t, root, "replacement/go.mod", "module example.com/replaced\n\ngo 1.25\n")
				write(t, root, "replacement/pkg/pkg.go", "package replacedpkg\nimport \"fmt\"\nfunc Print() { fmt.Println(\"replaced\") }\n")
				dir = filepath.Join(root, "app")
				t.Setenv("GOWORK", filepath.Join(root, "go.work"))
				source = "import (\n u \"example.com/used/pkg\"\n r \"example.com/replaced/pkg\"\n)\nu.Print()\nr.Print()\n"
				want = "used\nreplaced\n"
			case "module-vendor":
				write(t, root, "go.mod", "module example.com/app\n\ngo 1.25\n\nrequire example.com/dep v1.0.0\n")
				write(t, root, "vendor/modules.txt", "# example.com/dep v1.0.0\n## explicit; go 1.25\nexample.com/dep/pkg\n")
				write(t, root, "vendor/example.com/dep/pkg/pkg.go", "package vendored\nimport \"fmt\"\nfunc Print() { fmt.Println(\"vendor\") }\n")
				t.Setenv("GOFLAGS", "-mod=vendor")
				source = "import \"example.com/dep/pkg\"\nvendored.Print()\n"
				want = "vendor\n"
			case "gopath", "gopath-vendor":
				dir = filepath.Join(root, "src/app")
				dep := "src/example.com/dep/pkg/pkg.go"
				if kind == "gopath-vendor" {
					dep = "src/app/vendor/example.com/dep/pkg/pkg.go"
				}
				write(t, root, dep, "package legacy\nimport \"fmt\"\nfunc Print() { fmt.Println(\"gopath\") }\n")
				t.Setenv("GO111MODULE", "off")
				t.Setenv("GOPATH", root)
				source = "import \"example.com/dep/pkg\"\nlegacy.Print()\n"
				want = "gopath\n"
			}
			write(t, dir, "main.bpp", source)
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			var baselineOut, baselineErr bytes.Buffer
			runner, err := interp.New(interp.Dir(dir), interp.Lang(syntax.LangBashPP), interp.StdIO(nil, &baselineOut, &baselineErr), interp.Env(expand.ListEnviron(os.Environ()...)))
			if err != nil {
				t.Fatal(err)
			}
			if err = runner.Run(ctx, parse(t, source, "main.bpp")); err != nil || baselineOut.String() != want || baselineErr.Len() != 0 {
				t.Fatalf("source baseline stdout=%q stderr=%q status=%v; want %q / empty / 0", baselineOut.String(), baselineErr.String(), err, want)
			}
			result, err := lower.Compile(parse(t, source, "main.bpp"), lower.Options{Dir: dir})
			if err != nil {
				t.Fatalf("Compile in source-only %s: %v", kind, err)
			}
			unchanged, err := os.ReadFile(filepath.Join(dir, "main.bpp"))
			if err != nil || string(unchanged) != source {
				t.Fatalf("source changed: %v", err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if strings.HasSuffix(entry.Name(), ".go") {
					t.Fatalf("resolution created root Go source %s", entry.Name())
				}
			}
			// The artifact build occupies its own hidden child directory, preserving
			// module/internal/vendor ancestry without adding a package to go list ./....
			artifact := filepath.Join(dir, ".compiled")
			write(t, artifact, "main.go", string(result.Source))
			binary := filepath.Join(t.TempDir(), "program")
			command := exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-o", binary, filepath.Join(artifact, "main.go"))
			command.Dir = dir
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("artifact build: %v\n%s\n%s", err, output, result.Source)
			}
			if err := os.RemoveAll(artifact); err != nil {
				t.Fatal(err)
			}
			// Execution must not depend on either original or generated source.
			if err := os.Remove(filepath.Join(dir, "main.bpp")); err != nil {
				t.Fatal(err)
			}
			var out, stderr bytes.Buffer
			command = exec.CommandContext(ctx, binary)
			command.Dir = dir
			command.Env = []string{"PATH=/no-tools"}
			command.Stdout, command.Stderr = &out, &stderr
			if err := command.Run(); err != nil || out.String() != baselineOut.String() || stderr.String() != baselineErr.String() {
				t.Fatalf("artifact stdout=%q stderr=%q status=%v; source %q / %q / 0", out.String(), stderr.String(), err, baselineOut.String(), baselineErr.String())
			}
		})
	}
}
