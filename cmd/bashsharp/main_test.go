package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"github.com/bashsharp/bashsharp/front"
)

func TestGoSourceReexecPlan(t *testing.T) {
	executable := filepath.Join(t.TempDir(), "bashsharp")
	invocation := []string{
		"bashsharp", "--bashsharp", "--source=go", "--go-version=1.25",
		"--go-file=main.go", "--go-package=example/p=p.go,q.go",
		"--go-package-asm=example/p=p.s", "--go-import-base=example",
		"--go-import-path=example/p.test", "--go-test-builtins", "--go-test-main",
		"main.go", "--", "-test.v", "-test.run=TestOne",
	}
	want := []string{
		executable, "--bashsharp", "--source=go", "--go-version=1.25",
		"--go-file=main.go", "--go-package=example/p=p.go,q.go",
		"--go-package-asm=example/p=p.s", "--go-import-base=example",
		"--go-import-path=example/p.test", "--go-test-builtins", "--go-test-main",
		"main.go", "--",
	}
	if got := goSourceReexecPlan(executable, invocation); !reflect.DeepEqual(got, want) {
		t.Fatalf("goSourceReexecPlan() = %#v, want %#v", got, want)
	}
}

func TestGoTypesParserDiagnosticsFlagWiring(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "original.go")
	if err := os.WriteFile(source, []byte("package p\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	originalLoad := front.GoSourceLoad
	t.Cleanup(func() { front.GoSourceLoad = originalLoad })
	for _, tc := range []struct {
		name string
		args []string
		want bool
	}{
		{"default stays disabled", nil, false},
		{"flag enables diagnostics", []string{"--go-types-parser-diagnostics"}, true},
		{"true spelling enables diagnostics", []string{"--go-types-parser-diagnostics=true"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			front.GoSourceLoad = func(_ []front.GoSourceFile, opts front.GoSourceOptions) (*front.GoSourceProgram, error) {
				called = true
				if opts.GoTypesParserDiagnostics != tc.want {
					t.Fatalf("GoTypesParserDiagnostics = %v, want %v", opts.GoTypesParserDiagnostics, tc.want)
				}
				return nil, errors.New("stop after option capture")
			}
			args := append([]string{"bashsharp", "--bashsharp", "--source=go"}, tc.args...)
			args = append(args, source)
			if got := run(args); got != 2 || !called {
				t.Fatalf("run = %d, loader called = %v; want 2, true", got, called)
			}
		})
	}
}

func TestGoPackageAssemblyFlagWiring(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "p.go")
	assembly := filepath.Join(dir, "p.s")
	main := filepath.Join(dir, "main.go")
	for name, data := range map[string]string{source: "package p\n", assembly: "", main: "package main\n"} {
		if err := os.WriteFile(name, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	originalLoad := front.GoSourceLoad
	t.Cleanup(func() { front.GoSourceLoad = originalLoad })
	called := false
	front.GoSourceLoad = func(_ []front.GoSourceFile, opts front.GoSourceOptions) (*front.GoSourceProgram, error) {
		called = true
		if len(opts.Packages) != 1 || opts.Packages[0].Path != "example/p" || opts.Packages[0].SourceDir != dir || len(opts.Packages[0].CompanionFiles) != 1 || opts.Packages[0].CompanionFiles[0] != "p.s" {
			t.Fatalf("packages = %+v", opts.Packages)
		}
		return nil, errors.New("stop after option capture")
	}
	if got := run([]string{"bashsharp", "--bashsharp", "--source=go", "--go-package-asm=example/p=" + assembly, "--go-package=example/p=" + source, main}); got != 2 || !called {
		t.Fatalf("run = %d, loader called = %v; want 2, true", got, called)
	}
}

// TestCheckWithoutSourceSelectsByContent: --check with no --source is the
// semantic check of whatever the input is, and never runs a body.
func TestCheckWithoutSourceSelectsByContent(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "ran")
	write := func(name, body string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	goBody := func(extra string) string {
		return "//go:build norun\n\npackage main\n\nimport \"os\"\n\nfunc init() { os.WriteFile(" + strconv.Quote(marker) + ", nil, 0o644) }\n\nfunc main() { for {} }\n" + extra
	}
	cases := []struct {
		name, body string
		want       int
	}{
		{"go unit never runs", goBody(""), 0},
		{"go semantic error", "package main\n\nfunc main() { var x int = \"s\"; _ = x }\n", 2},
		{"shell never runs", "touch " + marker + "\nwhile true; do :; done\n", 0},
		{"shell semantic error", "touch " + marker + "\nfunc deref(p *int) int { return *p }\n", 2},
		{"shell syntax error", "touch " + marker + "\nif then fi (\n", 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := write("p.bsh", tc.body)
			if got := run([]string{"bashsharp", "--check", path}); got != tc.want {
				t.Errorf("run --check = %d, want %d", got, tc.want)
			}
			if _, err := os.Stat(marker); err == nil {
				t.Fatal("--check executed the program body")
			}
		})
	}
	t.Run("a .go file is checked, not compiled and run", func(t *testing.T) {
		path := write("p.go", goBody(""))
		if got := run([]string{"bashsharp", "--check", path}); got != 0 {
			t.Errorf("run --check = %d, want 0", got)
		}
		if _, err := os.Stat(marker); err == nil {
			t.Fatal("--check executed the program body")
		}
	})
	t.Run("explicit --source=sh keeps the refusal", func(t *testing.T) {
		if got := run([]string{"bashsharp", "--source=sh", "--check", write("q.bsh", "echo\n")}); got != 2 {
			t.Errorf("run = %d, want 2", got)
		}
	})
}

func withStdin(t *testing.T, content string) {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
	f.Seek(0, 0)
	old := os.Stdin
	os.Stdin = f
	t.Cleanup(func() { os.Stdin = old; f.Close() })
}

func TestCheckDashAndEmptyCommand(t *testing.T) {
	withStdin(t, "package main\n\nfunc main() { x }\n")
	if got := run([]string{"bashsharp", "--check", "-"}); got != 2 {
		t.Errorf("--check - with piped bad Go = %d, want 2", got)
	}
	withStdin(t, "echo ok\n")
	if got := run([]string{"bashsharp", "--check", "-"}); got != 0 {
		t.Errorf("--check - with piped shell = %d, want 0", got)
	}
	// An empty -c must neither read stdin (bad content here) nor hang.
	withStdin(t, "func d(p *int) int { return *p }\n")
	if got := run([]string{"bashsharp", "--check", "-c", ""}); got != 0 {
		t.Errorf("--check -c '' = %d, want 0", got)
	}
	if got := run([]string{"bashsharp", "--source=sh", "--check", "-c", ""}); got != 2 {
		t.Errorf("explicit --source=sh must still refuse, got %d", got)
	}
}
