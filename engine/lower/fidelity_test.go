package lower_test

import (
	"bytes"
	"go/format"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/lower"
)

// Sprint 152 decision D1: a Go-only input lowers to itself. The reproducers
// under testdata/sprint152/fidelity each isolate one rewrite class (spike F,
// FINDINGS.md); a class is closed when its generated Go, after gofmt and
// dropping the package clause, the "// lower:N" markers, their gofmt "//"
// separators and the "//line" directives, is byte-identical to the input.
// One layout detail belongs to the directives: gofmt separates a top-level
// declaration from a comment group before it (a doc comment or a
// free-floating one alike) with a blank line, so a per-declaration //line
// directive costs a blank line between adjacent declarations that the input
// may not have. The comparison therefore takes both sides with a blank line
// before every top-level declaration.
// Classes not yet closed are listed in fidelityOpen and are still exercised
// so that their output stays gofmt-stable.
//
// C1 stays open by design (S152.1): the converter carries only the //go:
// directives of a declaration into Stmt.Comments (gosource/directives.go);
// no other comment group reaches the Bash++ tree, so the emitter has nothing
// to write. Closing it means attaching every ast.CommentGroup — doc, inline,
// trailing, end-of-block and the pre-package header — in the converter, with
// the leading/trailing distinction reconstructed from positions (Comment has
// only its position and text), and blocks no row: asmcheck and // ERROR
// patterns are read from the original file.
var fidelityOpen = map[string]bool{
	"comments-dropped": true, // C1
}

// fidelityLanded records, for classes whose emitter rewrite is already
// removed but whose reproducer still carries a class that is open, the
// spellings that rewrite used to emit; none may appear in the output.
var fidelityLanded = map[string][]string{
	"main-rename":     {"sourceMain"},                                      // C5
	"sink-statements": {"_ = "},                                            // C2
	"type-assertion":  {"MustValue", "MustAssertOK", "Assert[", "import "}, // C9
}

func TestGoSourceFidelity(t *testing.T) {
	dir := filepath.Join("testdata", "sprint152", "fidelity")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, ".generated.go") {
			continue
		}
		t.Run(strings.TrimSuffix(name, ".go"), func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			program, err := gosource.Parse(bytes.NewReader(data), name, gosource.Options{RunMain: true})
			if err != nil {
				t.Fatal(err)
			}
			result, err := lower.Compile(program.File, lower.Options{Origin: name})
			if err != nil {
				t.Fatal(err)
			}
			// Byte-determinism gate: the emitter's own output is gofmt-stable.
			formatted, err := format.Source(result.Source)
			if err != nil {
				t.Fatalf("generated Go is not gofmt-parseable: %v\n%s", err, result.Source)
			}
			if !bytes.Equal(formatted, result.Source) {
				t.Errorf("generated != gofmt(generated)\n--- generated\n%s\n--- gofmt\n%s", result.Source, formatted)
			}
			for _, landed := range fidelityLanded[strings.TrimSuffix(name, ".go")] {
				if bytes.Contains(result.Source, []byte(landed)) {
					t.Errorf("removed rewrite still emits %q\n%s", landed, result.Source)
				}
			}
			want := fidelityNormalize(t, data)
			got := fidelityNormalize(t, result.Source)
			if want != got {
				if fidelityOpen[strings.TrimSuffix(name, ".go")] {
					t.Skipf("class still open\n--- want\n%s\n--- got\n%s", want, got)
				}
				t.Errorf("generated Go is not the input\n--- want\n%s\n--- got\n%s\n--- raw\n%s", want, got, result.Source)
			} else if fidelityOpen[strings.TrimSuffix(name, ".go")] {
				t.Errorf("class is closed; drop it from fidelityOpen")
			}
		})
	}
}

// fidelityNormalize gofmts src and drops the package clause, the generated
// header, the "// lower:N" markers with their "//" separators and the
// "//line" directives, then puts a blank line before every top-level
// declaration (and the comment group attached to it).
func fidelityNormalize(t *testing.T, src []byte) string {
	t.Helper()
	formatted, err := format.Source(src)
	if err != nil {
		t.Fatalf("gofmt: %v\n%s", err, src)
	}
	var out []string
	marker := false
	for _, line := range strings.Split(string(formatted), "\n") {
		if fidelityDecl(line) {
			start := len(out)
			for start > 0 && strings.HasPrefix(out[start-1], "//") {
				start--
			}
			if start > 0 && out[start-1] != "" {
				out = append(out[:start], append([]string{""}, out[start:]...)...)
			}
		}
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "// lower:"):
			marker = true
			continue
		case marker && trimmed == "//":
			continue
		case strings.HasPrefix(trimmed, "//line "):
			marker = false
			continue
		case strings.HasPrefix(trimmed, "package "):
			marker = false
			continue
		case trimmed == "// Code generated by github.com/bashsharp/bashsharp/engine/lower. DO NOT EDIT.":
			continue
		}
		marker = false
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n")) + "\n"
}

// fidelityDecl reports a line that opens a top-level declaration.
func fidelityDecl(line string) bool {
	for _, kw := range []string{"func ", "type ", "var ", "const ", "import "} {
		if strings.HasPrefix(line, kw) {
			return true
		}
	}
	return false
}

// TestGoSourceDirectives is the lower/ half of spike F class C1: a //go:
// directive the converter attaches to a declaration (Stmt.Comments, the
// //go:embed path) is emitted on that declaration. The converter attaches
// every func and var directive itself (gosource/directives.go).
func TestGoSourceDirectives(t *testing.T) {
	src := []byte("package main\n\n//go:noinline\nfunc F(x int) int {\n\treturn x\n}\n\n//go:norace\nfunc main() {\n\tvar x int\n\tprintln(F(x))\n}\n")
	program, err := gosource.Parse(bytes.NewReader(src), "directives.go", gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	result, err := lower.Compile(program.File, lower.Options{Origin: "directives.go"})
	if err != nil {
		t.Fatal(err)
	}
	formatted, err := format.Source(result.Source)
	if err != nil || !bytes.Equal(formatted, result.Source) {
		t.Errorf("generated != gofmt(generated): %v\n%s", err, result.Source)
	}
	for _, want := range []string{"//go:noinline\n//line directives.go:4:1\nfunc F(", "//go:norace\n//line directives.go:9:1\nfunc main("} {
		if !bytes.Contains(result.Source, []byte(want)) {
			t.Errorf("missing %q\n%s", want, result.Source)
		}
	}
	if want, got := fidelityNormalize(t, src), fidelityNormalize(t, result.Source); want != got {
		t.Errorf("generated Go is not the input\n--- want\n%s\n--- got\n%s", want, got)
	}
}
