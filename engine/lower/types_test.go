//go:build full

package lower

import (
	"bytes"
	"context"
	"errors"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func parseTypeFixture(t *testing.T, source string) *syntax.File {
	t.Helper()
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(source), "types.bpp")
	if err != nil {
		t.Fatal(err)
	}
	return f
}
func typeEmitter() *emitter { return &emitter{funcs: map[string]bool{}, scopes: []map[string]bool{{}}} }

// These are helper-level integration tests, not claims that Compile dispatch
// accepts the entire program. All type declarations and composite initializer
// expressions are emitted from the positioned Bash++ AST. The remaining Go-
// compatible body is shared verbatim by the interpreter and artifact runner.
func TestTypeHelpersArtifactParity(t *testing.T) {
	cases := []struct{ name, declarations, initializers, body string }{
		{"copy-and-alias", "", `a := [1][]int{{1}}
b := [1][]int{{2}}
s := []int{3}
m := map[string]int{"x": 5}
`, `b = a
b[0] = []int{2}
t := s
t[0] = 4
n := m
n["x"] = 6
println(a[0][0], b[0][0], s[0], t[0], m["x"], n["x"])
`},
		{"embedding-pointer-and-method-set", `type Leaf struct { N int }
type Outer struct { *Leaf }
type Reader interface { Value() int }
type Both interface { Reader; Set(int) }
`, `leaf := Leaf{N: 2}
`, `o := Outer{Leaf: &leaf}
var r Reader = o
var both Both = o
both.Set(7)
x, ok := r.(Outer)
result := r.Value()
println(result, x.N, ok)
`},
		{"generic-type-alias-and-builtins", `type Number interface { ~int }
type Vec[T any] []T
type Alias = Vec[int]
`, `v := Vec[int]{2, 3}
m := map[string][]int{"a": {4, 5}}
`, `var a Alias = v
a[0] = 8
b := make([]int, 2, 4)
n := copy(b, a)
b = append(b, 9)
delete(m, "a")
length := len(b)
capacity := cap(b)
mapLength := len(m)
low := min(b[0], b[1])
high := max(1, 2)
println(v[0], n, length, capacity, mapLength, low, high)
clear(b)
println(b[0], b[1], b[2])
`},
		{"arrays-slices-and-map-keys", "", `a := [...]int{1: 3, 5}
s := []int{}
m := map[string]int{"x": 7}
`, `var missing []int
view := a[:2:2]
view[1] = 9
capacity := cap(view)
emptyNil := s == nil
missingNil := missing == nil
println(a[1], a[2], capacity, emptyNil, missingNil, m["x"])
`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			methods := ""
			if tc.name == "embedding-pointer-and-method-set" {
				methods = "func (v Leaf) Value() int { result := v.N; return result }\nfunc (p *Leaf) Set(n int) { p.N = n }\n"
			}
			input := tc.declarations + methods + "func main() {\n" + tc.initializers + tc.body + "}\nmain()\n"
			tree := parseTypeFixture(t, input)
			e := typeEmitter()
			var declarations, initializers strings.Builder
			for _, stmt := range tree.Stmts {
				if decl, ok := stmt.Cmd.(*syntax.BashPPDecl); ok {
					text, err := e.typeDecl(decl)
					if err != nil {
						t.Fatal(err)
					}
					declarations.WriteString(text + "\n")
				}
				if f, ok := stmt.Cmd.(*syntax.BashPPFuncDecl); ok && f.Name.Value == "main" {
					count := len(parseTypeFixture(t, tc.initializers).Stmts)
					for _, s := range f.Body.Stmts[:count] {
						decl, ok := s.Cmd.(*syntax.BashPPShortDecl)
						if !ok {
							t.Fatalf("initializer is %T", s.Cmd)
						}
						literal, ok := decl.Expr.(*syntax.BashPPCompositeLit)
						if !ok {
							t.Fatalf("initializer expression is %T", decl.Expr)
						}
						text, err := e.compositeExpr(literal)
						if err != nil {
							t.Fatal(err)
						}
						initializers.WriteString(strings.Join(names(decl.Lhs), ", ") + " := " + text + "\n")
					}
				}
			}
			generated := "package main\nimport \"fmt\"\nfunc println(args ...any) { fmt.Println(args...) }\n" + declarations.String() + methods + "func main() {\n" + initializers.String() + tc.body + "}\n"
			dir := t.TempDir()
			sourcePath := filepath.Join(dir, "generated.go")
			if err := os.WriteFile(sourcePath, []byte(generated), 0600); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			binary := filepath.Join(dir, "program")
			build := exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-o", binary, sourcePath)
			build.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOWORK=off")
			if out, err := build.CombinedOutput(); err != nil {
				t.Fatalf("build: %v\n%s\n%s", err, out, generated)
			}
			if err := os.Remove(sourcePath); err != nil {
				t.Fatal(err)
			}
			run := exec.CommandContext(ctx, binary)
			run.Dir = t.TempDir()
			run.Env = []string{"PATH=/no-tools"}
			var got, gotErr bytes.Buffer
			run.Stdout, run.Stderr = &got, &gotErr
			if err := run.Run(); err != nil {
				t.Fatalf("artifact: %v %s", err, gotErr.String())
			}
			var want, wantErr bytes.Buffer
			runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.StdIO(nil, &want, &wantErr), interp.Dir(run.Dir), interp.Env(expand.ListEnviron("PATH=/no-tools")))
			if err != nil {
				t.Fatal(err)
			}
			if err := runner.Run(ctx, tree); err != nil {
				t.Fatalf("interpreter: %v %s", err, wantErr.String())
			}
			if got.String() != want.String() || gotErr.String() != wantErr.String() {
				t.Fatalf("artifact=(%q,%q) interpreter=(%q,%q)", got.String(), gotErr.String(), want.String(), wantErr.String())
			}
		})
	}
}

func TestTypeHelpersNativeChecking(t *testing.T) {
	cases := []struct {
		name, declaration, use string
		valid                  bool
	}{
		{"alias", "type Count int\ntype Alias = Count", "var a Alias; var b Count = a; _ = b", true},
		{"defined-type", "type Count int\ntype Other int", "var a Other; var b Count = a; _ = b", false},
		{"constraint", "type Number interface { ~int }\ntype Vec[T Number] []T", "var a Vec[string]; _ = a", false},
		{"constraint-valid", "type Number interface { ~int }\ntype Vec[T Number] []T", "var a Vec[int]; _ = a", true},
		{"noncomparable-map-key", "type Bad map[[]int]int", "", false},
		{"ambiguous-field", "type A struct { N int }\ntype B struct { N int }\ntype C struct { A; B }", "var c C; _ = c.N", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := typeEmitter()
			var declarations strings.Builder
			for _, s := range parseTypeFixture(t, tc.declaration+"\n").Stmts {
				text, err := e.typeDecl(s.Cmd.(*syntax.BashPPDecl))
				if err != nil {
					t.Fatal(err)
				}
				declarations.WriteString(text + "\n")
			}
			fs := token.NewFileSet()
			f, err := parser.ParseFile(fs, "generated.go", "package p\n"+declarations.String()+"func test() {"+tc.use+"}", 0)
			if err != nil {
				t.Fatal(err)
			}
			_, err = (&types.Config{Importer: importer.Default()}).Check("p", fs, []*ast.File{f}, nil)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v check=%v", tc.valid, err)
			}
		})
	}
}

func TestTypeHelpersDoNotLeakMethodParameterBindings(t *testing.T) {
	e := typeEmitter()
	tree := parseTypeFixture(t, "type I interface { M(value int) (result string) }\n")
	text, err := e.typeDecl(tree.Stmts[0].Cmd.(*syntax.BashPPDecl))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(text, "M(") != 1 {
		t.Fatal(text)
	}
	if e.known("value") || e.known("result") {
		t.Fatal("interface signature leaked local bindings")
	}
}
func TestTypeHelpersRejectInvalidCommittedTypes(t *testing.T) {
	e := typeEmitter()
	for _, text := range []string{"f()", "1 + 2", "int; var stolen int", "[]f()"} {
		_, err := e.typeSpelling(&syntax.Lit{Value: text}, text)
		var list ErrorList
		if !errors.As(err, &list) || len(list) != 1 || list[0].Code != CodeType {
			t.Fatalf("%q: %v", text, err)
		}
	}
}

func TestGenericFunctionTypeSetInference(t *testing.T) {
	e := typeEmitter()
	// The source language escapes a union bar in this committed generic header.
	tree := parseTypeFixture(t, "func identity[T ~int\\|string](value T) T { return value }\n")
	f := tree.Stmts[0].Cmd.(*syntax.BashPPFuncDecl)
	params, err := e.typeParams(f.TypeParams)
	if err != nil {
		t.Fatal(err)
	}
	if params != "[T ~int | string]" {
		t.Fatal(params)
	}
	for _, tc := range []struct {
		use   string
		valid bool
	}{
		{`var integer int = identity(3); var text string = identity("x"); _, _ = integer, text`, true},
		{`_ = identity(true)`, false},
		{`type Count int; var c Count; var same Count = identity(c); _ = same`, true},
	} {
		fs := token.NewFileSet()
		file, err := parser.ParseFile(fs, "generic.go", "package p\nfunc identity"+params+"(value T) T { return value }\nfunc test() {"+tc.use+"}", 0)
		if err != nil {
			t.Fatal(err)
		}
		_, err = (&types.Config{}).Check("p", fs, []*ast.File{file}, nil)
		if (err == nil) != tc.valid {
			t.Fatalf("%s: %v", tc.use, err)
		}
	}
}
func TestTypeHelpersSelectorAndAssertion(t *testing.T) {
	e := typeEmitter()
	e.bind("value")
	tree := parseTypeFixture(t, "func check() {\nfield := value.N\nasserted := value.(int)\n}\n")
	stmts := tree.Stmts[0].Cmd.(*syntax.BashPPFuncDecl).Body.Stmts
	field := stmts[0].Cmd.(*syntax.BashPPShortDecl).Expr.(*syntax.BashPPSelectorExpr)
	got, err := e.selectorExpr(field)
	if err != nil || got != "(value).N" {
		t.Fatalf("%s: %v", got, err)
	}
	assertion := stmts[1].Cmd.(*syntax.BashPPShortDecl).Expr.(*syntax.BashPPTypeAssertExpr)
	got, err = e.typeAssertExpr(assertion)
	if err != nil || got != "(value).(int)" {
		t.Fatalf("%s: %v", got, err)
	}
}
func TestLegacySignatureTypes(t *testing.T) {
	e := typeEmitter()
	for _, text := range []string{"[]int", "map[string]*int", "func(int, ...string) (bool, error)", "<-chan int", "chan<- string", "Box[int]"} {
		typ, err := e.fieldType(&syntax.BashPPField{FieldType: &syntax.Lit{Value: text}})
		if err != nil || typ != text {
			t.Fatalf("%q: %q %v", text, typ, err)
		}
	}
}
