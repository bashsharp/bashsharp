package lower_test

import (
	"bytes"
	"errors"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/lower"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Sprint 162 S162.4 wave 1a: a body-less function declaration in Go source
// is a compile-time fact — assembly, a //go:linkname or a //go:wasmimport
// supplies the body and gc decides whether one is present — so the emitter
// passes it through exactly as written instead of refusing with
// LOWER-EUNSUPPORTED. The reproducers under testdata/sprint162/bodyless are
// outside the corpus: three positive controls (a plain assembly-backed func,
// body-less methods, directive-carrying declarations), the negative (a
// generic body-less func, which gc rejects and the checker rejects before
// lowering) and the fidelity control (a func with a body is unchanged).
//
// Each positive is golden-compared against its .generated.go
// (SPRINT162_UPDATE=1 rewrites the goldens), must lower to itself under the
// Sprint 152 D1 normalisation, and its body-less declarations must survive
// both textually (the exact source line, under its directives) and
// structurally (go/parser sees a FuncDecl with no body and the directives in
// its doc group). Whether the generated file then compiles is the backend's
// business: cmd/go adds -complete to a package with no non-Go files and gc
// then reports "missing function body", exactly as it does for the input.
var bodylessDecls = map[string][]string{
	"asm-backed": {"func Add(a, b int) int"},
	"method":     {"func (v Vec) Dot(w Vec) float64", "func (*Vec) Scale(k float64)"},
	"directives": {"func Load(p *uint64) uint64", "func Store(p *uint64, v uint64)", "func nanotime() int64"},
	"with-body":  nil,
}

// bodylessDirectives lists, per body-less declaration, the directive lines
// that must precede it in the output in source order.
var bodylessDirectives = map[string][]string{
	"func Load(p *uint64) uint64":     {"//go:noescape"},
	"func Store(p *uint64, v uint64)": {"//go:nosplit", "//go:noescape"},
	"func nanotime() int64":           {"//go:linkname nanotime runtime.nanotime"},
	"func Add(a, b int) int":          nil,
	"func (v Vec) Dot(w Vec) float64": nil,
	"func (*Vec) Scale(k float64)":    nil,
}

func TestGoSourceBodylessPassThrough(t *testing.T) {
	dir := filepath.Join("testdata", "sprint162", "bodyless")
	for name, decls := range bodylessDecls {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(dir, name+".go"))
			if err != nil {
				t.Fatal(err)
			}
			program, err := gosource.Parse(bytes.NewReader(data), name+".go", gosource.Options{RunMain: true})
			if err != nil {
				t.Fatal(err)
			}
			result, err := lower.Compile(program.File, lower.Options{Origin: name + ".go"})
			if err != nil {
				t.Fatalf("lowering refused a body-less declaration: %v", err)
			}
			got := result.Source

			// Golden: the emitted bytes are the recorded ones.
			golden := filepath.Join(dir, name+".generated.go")
			if os.Getenv("SPRINT162_UPDATE") == "1" {
				if err := os.WriteFile(golden, got, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(want, got) {
				t.Errorf("generated Go differs from %s (SPRINT162_UPDATE=1 to rewrite)\n--- want\n%s\n--- got\n%s", golden, want, got)
			}

			// gofmt-stable, and identical to the input under the D1 rule.
			formatted, err := format.Source(got)
			if err != nil {
				t.Fatalf("generated Go is not gofmt-parseable: %v\n%s", err, got)
			}
			if !bytes.Equal(formatted, got) {
				t.Errorf("generated != gofmt(generated)\n--- generated\n%s\n--- gofmt\n%s", got, formatted)
			}
			if in, out := fidelityNormalize(t, data), fidelityNormalize(t, got); in != out {
				t.Errorf("generated Go is not the input\n--- want\n%s\n--- got\n%s", in, out)
			}

			// Textual survival: the declaration line as written, directly
			// under its directives and the //line directive.
			lines := strings.Split(string(got), "\n")
			for _, decl := range decls {
				at := -1
				for i, line := range lines {
					if line == decl {
						at = i
						break
					}
				}
				if at < 0 {
					t.Errorf("declaration %q does not survive\n%s", decl, got)
					continue
				}
				if at+1 < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[at+1]), "{") {
					t.Errorf("declaration %q grew a body\n%s", decl, got)
				}
				if at == 0 || !strings.HasPrefix(lines[at-1], "//line "+name+".go:") {
					t.Errorf("declaration %q is not under its //line directive\n%s", decl, got)
					continue
				}
				directives := bodylessDirectives[decl]
				j := at - 2 // skip the declaration's //line directive
				for i := len(directives) - 1; i >= 0; i-- {
					for j >= 0 && strings.HasPrefix(lines[j], "//line ") {
						j--
					}
					if j < 0 || lines[j] != directives[i] {
						t.Errorf("directive %q of %q is not kept above it\n%s", directives[i], decl, got)
						continue
					}
					j--
				}
			}

			// Structural survival: go/parser agrees the declaration has no
			// body and carries its directives.
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, name+".generated.go", got, parser.ParseComments)
			if err != nil {
				t.Fatalf("go/parser rejects the generated Go: %v\n%s", err, got)
			}
			bodyless := map[string]*ast.FuncDecl{}
			for _, d := range file.Decls {
				if fn, ok := d.(*ast.FuncDecl); ok && fn.Body == nil {
					bodyless[fn.Name.Name] = fn
				}
			}
			if len(bodyless) != len(decls) {
				t.Errorf("want %d body-less declarations, go/parser sees %d\n%s", len(decls), len(bodyless), got)
			}
			for _, decl := range decls {
				name := declName(decl)
				fn := bodyless[name]
				if fn == nil {
					t.Errorf("go/parser does not see %q body-less", name)
					continue
				}
				var doc []string
				if fn.Doc != nil {
					for _, c := range fn.Doc.List {
						if strings.HasPrefix(c.Text, "//go:") {
							doc = append(doc, c.Text)
						}
					}
				}
				if want := bodylessDirectives[decl]; strings.Join(doc, "\n") != strings.Join(want, "\n") {
					t.Errorf("directives of %q: want %q, got %q", name, want, doc)
				}
			}
		})
	}
}

// declName is the function name of a declaration line such as
// "func (v Vec) Dot(w Vec) float64".
func declName(decl string) string {
	rest := strings.TrimPrefix(decl, "func ")
	if strings.HasPrefix(rest, "(") {
		_, rest, _ = strings.Cut(rest, ") ")
	}
	name, _, _ := strings.Cut(rest, "(")
	return name
}

// TestGoSourceBodylessGeneric is the negative control: gc reports a generic
// body-less declaration as an error, and so does the checker the Go-source
// front end runs before lowering — the pass-through never sees it.
func TestGoSourceBodylessGeneric(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "sprint162", "bodyless", "generic.go"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = gosource.Parse(bytes.NewReader(data), "generic.go", gosource.Options{RunMain: true})
	if err == nil {
		t.Fatal("a generic body-less declaration was accepted")
	}
	if want := "generic.go:3:6: generic function is missing function body"; !strings.Contains(err.Error(), want) {
		t.Errorf("want %q, got %q", want, err)
	}
}

// TestGoSourceBodylessRuntimeRefused: the runtime (execution) path wraps a
// function body it does not have, so it keeps refusing a body-less
// declaration rather than emitting a wrapper around nothing.
func TestGoSourceBodylessRuntimeRefused(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "sprint162", "bodyless", "asm-backed.go"))
	if err != nil {
		t.Fatal(err)
	}
	program, err := gosource.Parse(bytes.NewReader(data), "asm-backed.go", gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = lower.Compile(program.File, lower.Options{Origin: "asm-backed.go", Entry: "Run"})
	var list lower.ErrorList
	if !errors.As(err, &list) || len(list) != 1 || list[0].Code != lower.CodeUnsupported || list[0].Msg != "function declaration without body" {
		t.Fatalf("want LOWER-EUNSUPPORTED function declaration without body, got %v", err)
	}
}

// TestBashPPBodylessUnspellable: a Bash++ script cannot spell a body-less
// function at all — the parser demands the body — so the pass-through is
// reachable from Go source only.
func TestBashPPBodylessUnspellable(t *testing.T) {
	src := "func f(x int) int\nf 1\n"
	_, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "bodyless.sh")
	if err == nil || !strings.Contains(err.Error(), "a { } body") {
		t.Fatalf("want the parser to demand a body, got %v", err)
	}
}
