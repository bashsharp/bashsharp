package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
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
			args := append([]string{"bashsharp", "--source=go"}, tc.args...)
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
	if got := run([]string{"bashsharp", "--source=go", "--go-package-asm=example/p=" + assembly, "--go-package=example/p=" + source, main}); got != 2 || !called {
		t.Fatalf("run = %d, loader called = %v; want 2, true", got, called)
	}
}
