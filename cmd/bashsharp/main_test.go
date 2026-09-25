package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/bashsharp/bashsharp/front"
)

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
