//go:build full

package lower_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/lower"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// TestGoSourceNativeConstExpressions drives the compiled native-Go boundary:
// an explicit grouped constant initializer must keep the expression Go wrote,
// including its import and lexical declaration uses. The interpreter remains
// free to evaluate the typed expression tree.
func TestGoSourceNativeConstExpressions(t *testing.T) {
	path := filepath.Join("testdata", "sprint171", "w3-fidelity", "native-constants", "main.go")
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	program, err := gosource.Parse(bytes.NewReader(source), path, gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	result, err := lower.Compile(program.File, lower.Options{Origin: path})
	if err != nil {
		t.Fatal(err)
	}
	generated := string(result.Source)
	want, err := os.ReadFile(filepath.Join(filepath.Dir(path), "main.golden"))
	if err != nil {
		t.Fatal(err)
	}
	if generated != string(want) {
		t.Errorf("generated Go differs from golden output\n--- want ---\n%s\n--- generated ---\n%s", want, generated)
	}
	for _, want := range []string{
		"unsafe.Sizeof(func()",
		"var first [iota]int",
		"unsafe.Sizeof([iota - 1]int{} == first)",
	} {
		if !strings.Contains(generated, want) {
			t.Errorf("generated Go lost %q\n--- generated ---\n%s", want, generated)
		}
	}
	for _, folded := range []string{
		"var first [1]int",
		"unsafe.Sizeof([2 - 1]int{} == first)",
	} {
		if strings.Contains(generated, folded) {
			t.Errorf("generated Go contains folded expression %q\n--- generated ---\n%s", folded, generated)
		}
	}
	dir := t.TempDir()
	generatedPath := filepath.Join(dir, "generated.go")
	if err := os.WriteFile(generatedPath, result.Source, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "run", generatedPath)
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOTOOLCHAIN=local")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated Go did not compile and run: %v\n%s\n--- generated ---\n%s", err, output, generated)
	}
}

// TestGoSourceNativeSingleConstCarrier pins lower's half of the native-source
// carrier contract. The interpreter expression may be folded, while Init
// retains the source text that compiled output must use.
func TestGoSourceNativeSingleConstCarrier(t *testing.T) {
	path := filepath.Join("testdata", "sprint171", "w3-fidelity", "native-constants", "single.go")
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	program, err := gosource.Parse(bytes.NewReader(source), path, gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	var declaration *syntax.BashPPDecl
	syntax.Walk(program.File, func(node syntax.Node) bool {
		if candidate, ok := node.(*syntax.BashPPDecl); ok && candidate.Kw.Value == "const" && candidate.Name.Value == "width" {
			declaration = candidate
		}
		return true
	})
	if declaration == nil || len(declaration.Init) != 1 {
		t.Fatal("front end did not produce the single const carrier")
	}
	// This is the requested gosource handoff: keep its folded InitExpr for
	// interpretation while carrying the written expression in Init.
	declaration.Init[0] = &syntax.Word{Parts: []syntax.WordPart{&syntax.Lit{Value: "len(values)", ValuePos: declaration.Name.End()}}}
	result, err := lower.Compile(program.File, lower.Options{Origin: path})
	if err != nil {
		t.Fatal(err)
	}
	generated := string(result.Source)
	if !strings.Contains(generated, "const width = len(values)") || strings.Contains(generated, "const width = 4") {
		t.Fatalf("compiled const did not select the written carrier:\n%s", generated)
	}
}

func TestGoSourceNativeConstNegativeControls(t *testing.T) {
	tests := []struct {
		name   string
		source string
		want   string
	}{
		{"implicit initializer", "package main\nconst ( one = 1; two )\nfunc main() { _ = two }\n", "two = 1"},
		{"bashpp unchanged", "const ( one = 1 + 2 )\nprintf '%s\\n' $one\n", "one = (1 + 2)"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var file *syntax.File
			if strings.HasPrefix(test.source, "package ") {
				program, err := gosource.Parse(strings.NewReader(test.source), "negative.go", gosource.Options{RunMain: true})
				if err != nil {
					t.Fatal(err)
				}
				file = program.File
			} else {
				file = parse(t, test.source, "negative.bpp")
			}
			result, err := lower.Compile(file, lower.Options{})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(result.Source), test.want) {
				t.Fatalf("generated Go does not contain %q:\n%s", test.want, result.Source)
			}
		})
	}
}
