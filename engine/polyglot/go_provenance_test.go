package polyglot

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func goProvenanceFixture(t *testing.T) (Go, string) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.local/embed\n\ngo 1.25\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "folder"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{"folder/file1.hash": "first", "folder/file2.hash": "second", "folder/single_file.txt": "single"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	environment, err := DiscoverEnvironment(EnvironmentRequest{Source: filepath.Join(root, "logging.bsh"), Language: "go"})
	if err != nil {
		t.Fatal(err)
	}
	return Go{Environment: &environment}, root
}

func TestGoDeclaredEmbedAssetsAndSourceLine(t *testing.T) {
	runtime, root := goProvenanceFixture(t)
	source := aggregateSource("go", []Block{{Filename: "logging.bsh", Line: 40, Source: `import (
	"embed"
	"log"
	"os"
)
//go:embed folder/single_file.txt
var single string
//go:embed folder/*.hash
var files embed.FS
func Read() string {
	log.New(os.Stderr, "", log.Lshortfile).Print("origin")
	a, _ := files.ReadFile("folder/file1.hash")
	b, _ := files.ReadFile("folder/file2.hash")
	return single + string(a) + string(b)
}
`}})
	exports, artifact, err := runtime.AnalyzeArtifact(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if len(exports) != 1 || exports[0].Name != "Read" {
		t.Fatalf("exports = %#v", exports)
	}
	for _, name := range []string{"folder/file1.hash", "folder/file2.hash", "folder/single_file.txt"} {
		if err := os.Remove(filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	plan := Plan{Language: "go", Artifact: artifact, Exports: exports}
	module := Start(plan, runtime)
	defer module.Close()
	result, err := module.Call(context.Background(), "Read")
	if err != nil {
		t.Fatal(err)
	}
	if result.Value != "singlefirstsecond" {
		t.Fatalf("result = %#v", result.Value)
	}
	if !strings.Contains(result.Stderr, "logging.bsh:50:") {
		t.Fatalf("logging origin = %q", result.Stderr)
	}
}

func TestGoEmbedAssetRefusals(t *testing.T) {
	runtime, root := goProvenanceFixture(t)
	for _, pattern := range []string{"folder/missing.hash", "../secret", "folder/../../secret"} {
		source := "import _ \"embed\"\n//go:embed " + pattern + "\nvar asset string\nfunc Read() string { return asset }\n"
		if _, _, err := runtime.AnalyzeArtifact(context.Background(), source); err == nil {
			t.Errorf("accepted %q", pattern)
		}
	}
	outside := filepath.Join(t.TempDir(), "outside.hash")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "folder", "escape.hash")); err == nil {
		source := "import _ \"embed\"\n//go:embed folder/*.hash\nvar asset embed.FS\n"
		if _, _, err := runtime.AnalyzeArtifact(context.Background(), source); err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Errorf("symlink refusal = %v", err)
		}
	}
}

func TestGoEmbedSourceOriginBinding(t *testing.T) {
	runtime, root := goProvenanceFixture(t)
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "single.txt"), []byte("other source"), 0o600); err != nil {
		t.Fatal(err)
	}
	block := Block{Language: "go", Alias: "g", Origin: filepath.Join(other, "source.bsh"), Filename: "source.bsh", Line: 1,
		Source: "import _ \"embed\"\n//go:embed single.txt\nvar asset string\nfunc Read() string { return asset }\n"}
	plans, err := Prepare(context.Background(), []Block{block}, map[string]Analyzer{"go": runtime})
	if err != nil {
		t.Fatal(err)
	}
	module := Start(plans[0], runtime)
	defer module.Close()
	result, err := module.Call(context.Background(), "Read")
	if err != nil || result.Value != "other source" {
		t.Fatalf("origin result = %#v, %v (environment root %s)", result, err, root)
	}
	second := block
	second.Origin = filepath.Join(root, "different.bsh")
	second.Source = "//go:embed folder/single_file.txt\nvar second string\n"
	if _, err := Prepare(context.Background(), []Block{block, second}, map[string]Analyzer{"go": runtime}); err == nil || !strings.Contains(err.Error(), "different source directories") {
		t.Fatalf("mixed-origin error = %v", err)
	}
}

// Keep the explicit-match and directory-walk rules tied to the selected Go
// toolchain. The staged names must match cmd/go's accepted source layout.
func TestGoEmbedPatternNativeParity(t *testing.T) {
	root := t.TempDir()
	for name, data := range map[string]string{
		"go.mod":  "module example.local/embedparity\n\ngo 1.25\n",
		".hidden": "hidden", "_item": "item", "folder/visible": "visible",
		"folder/.hidden": "nested hidden", "folder/_item": "nested item",
		"folder/nested/go.mod": "module example.local/nested\n\ngo 1.25\n",
		"folder/nested/asset":  "nested asset",
	} {
		filename := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(filename), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filename, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name, pattern string
		want          []string
		failure       bool
	}{
		{"explicit-dot", ".hidden", []string{".hidden"}, false},
		{"explicit-underscore", "_item", []string{"_item"}, false},
		{"glob-dot", ".h*", []string{".hidden"}, false},
		{"glob-underscore", "_it*", []string{"_item"}, false},
		{"walk", "folder", []string{"folder/visible"}, false},
		{"walk-all", "all:folder", []string{"folder/.hidden", "folder/_item", "folder/visible"}, false},
		{"nested-direct", "folder/nested/asset", nil, true},
		{"nested-glob", "folder/nested/*", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := "package main\nimport \"embed\"\n//go:embed " + tc.pattern + "\nvar files embed.FS\nfunc main() {}\n"
			if err := os.WriteFile(filepath.Join(root, "main.go"), []byte(source), 0o600); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command("go", "build", "-o", filepath.Join(t.TempDir(), "native"), ".")
			cmd.Dir = root
			output, nativeErr := cmd.CombinedOutput()
			scratch := t.TempDir()
			staged, err := stageGoEmbedAssets(source, root, filepath.Join(root, ".virtual"), scratch)
			if (nativeErr != nil) != tc.failure {
				t.Fatalf("native error = %v, want failure %v: %s", nativeErr, tc.failure, output)
			}
			if (err != nil) != tc.failure {
				t.Fatalf("stage error = %v, want failure %v (native: %s)", err, tc.failure, output)
			}
			if tc.failure {
				return
			}
			if len(staged) != len(tc.want) {
				t.Fatalf("staged = %v, want %v", staged, tc.want)
			}
			for _, name := range tc.want {
				if _, ok := staged[filepath.Join(root, ".virtual", filepath.FromSlash(name))]; !ok {
					t.Errorf("missing staged %s in %v", name, staged)
				}
			}
		})
	}
}
