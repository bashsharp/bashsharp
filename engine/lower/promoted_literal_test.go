//go:build full

package lower

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func promotedFixture(t *testing.T, source string) (*syntax.File, map[string]*syntax.BashPPDecl, *syntax.BashPPCompositeLit) {
	t.Helper()
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(source), "promoted.bpp")
	if err != nil {
		t.Fatal(err)
	}
	declared := map[string]*syntax.BashPPDecl{}
	var literal *syntax.BashPPCompositeLit
	syntax.Walk(file, func(node syntax.Node) bool {
		if decl, ok := node.(*syntax.BashPPDecl); ok && decl.Kw.Value == "type" {
			declared[decl.Name.Value] = decl
		}
		if short, ok := node.(*syntax.BashPPShortDecl); ok && literal == nil {
			literal, _ = short.Expr.(*syntax.BashPPCompositeLit)
		}
		return true
	})
	if literal == nil {
		t.Fatal("missing fixture literal")
	}
	return file, declared, literal
}
func TestPromotedLiteralArtifactParity(t *testing.T) {
	cases := []struct{ name, source, output string }{
		{"public", promotedPublicSource, `fmt.Printf("%s:%d:%s",g.Name,g.Depth,g.Label)`},
		{"generic", `type Leaf[T any] struct { Value T; Label string }
type Holder[T any] struct { Leaf[T] }
func main() {
 g := Holder[int]{Label: "generic", Value: 7}
 printf '%s:%s' g.Value g.Label
}
main()
`, `fmt.Printf("%d:%s",g.Value,g.Label)`},
		{"direct-precedence", `type Leaf struct { N int; Label string }
type Outer struct { Leaf; N int }
func main() {
 g := Outer{N: 9, Label: "direct"}
 printf '%s:%s:%s' g.N g.Leaf.N g.Label
}
main()
`, `fmt.Printf("%d:%d:%s",g.N,g.Leaf.N,g.Label)`},
		{"generic-alias-nested", `type Leaf[T any] struct { Values []T }
type Alias = Leaf[int]
type Outer struct { Alias; Name string }
func main() {
 g := Outer{Name: "alias", Values: []int{3, 4}}
 value := g.Values[1]
 printf '%s:%s' g.Name "$value"
}
main()
`, `fmt.Printf("%s:%d",g.Name,g.Values[1])`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "public" {
				if path := os.Getenv("BASHPP_PROMOTED_FIXTURE"); path != "" {
					source, err := os.ReadFile(path)
					if err != nil || string(source) != tc.source {
						t.Fatalf("external public fixture differs: %v", err)
					}
				}
			}
			file, declared, literal := promotedFixture(t, tc.source)
			var before bytes.Buffer
			if err := syntax.NewPrinter().Print(&before, file); err != nil {
				t.Fatal(err)
			}
			e := typeEmitter()
			e.prefix = "pl_"
			expression, handled, err := e.promotedCompositeExpr(literal, declared)
			if err != nil || !handled {
				t.Fatalf("handled=%v error=%v", handled, err)
			}
			var declarations strings.Builder
			for _, stmt := range file.Stmts {
				if decl, ok := stmt.Cmd.(*syntax.BashPPDecl); ok {
					text, err := e.typeDecl(decl)
					if err != nil {
						t.Fatal(err)
					}
					declarations.WriteString(text + "\n")
				}
			}
			source := "package main\nimport \"fmt\"\n" + declarations.String() + "func main(){g := " + expression + ";" + tc.output + "}\n"
			output := runPromotedArtifact(t, source)
			var stdout, stderr bytes.Buffer
			runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.StdIO(nil, &stdout, &stderr), interp.Env(expand.ListEnviron("PATH=/no-tools")))
			if err != nil {
				t.Fatal(err)
			}
			if err := runner.Run(context.Background(), file); err != nil || stderr.Len() != 0 {
				t.Fatalf("interpreter: %v %s", err, stderr.String())
			}
			if output != stdout.String() {
				t.Fatalf("artifact=%q interpreter=%q", output, stdout.String())
			}
			var after bytes.Buffer
			if err := syntax.NewPrinter().Print(&after, file); err != nil {
				t.Fatal(err)
			}
			if before.String() != after.String() {
				t.Fatal("helper mutated source AST")
			}
		})
	}
}
func runPromotedArtifact(t *testing.T, source string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "generated.go")
	binary := filepath.Join(dir, "program")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, path)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s\n%s", err, out, source)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	run := exec.CommandContext(ctx, binary)
	run.Dir = t.TempDir()
	run.Env = []string{"PATH=/no-tools"}
	out, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("artifact: %v %s", err, out)
	}
	return string(out)
}
func TestPromotedLiteralDiagnosticParity(t *testing.T) {
	cases := []struct{ name, decl, literal, code string }{
		{"ambiguous", "type A struct { N int }; type B struct { N int }; type Outer struct { A; B }", "Outer{N: 1}", "AMBIGUOUS"},
		{"duplicate", "type Outer struct { N int }", "Outer{N: 1, N: 2}", "DUPLICATE"},
		{"unknown", "type Outer struct { N int }", "Outer{Missing: 1}", "UNKNOWN"},
		{"mixed", "type Outer struct { N int; M int }", "Outer{N: 1, 2}", "MIXED"},
		{"pointer", "type Leaf struct { N int }; type Outer struct { *Leaf }", "Outer{N: 1}", "KEY-POINTER"},
		{"pointer-alias", "type Leaf struct { N int }; type P = *Leaf; type Outer struct { P }", "Outer{N: 1}", "KEY-POINTER"},
		{"conflict", "type Leaf struct { N int }; type Outer struct { Leaf }", "Outer{Leaf: Leaf{N: 1}, N: 2}", "KEY-CONFLICT"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source := tc.decl + "\nfunc main() {\n g := " + tc.literal + "\n}\nmain()\n"
			file, declared, literal := promotedFixture(t, source)
			_, handled, err := typeEmitter().promotedCompositeExpr(literal, declared)
			diagnostics, ok := err.(ErrorList)
			if !handled || !ok || len(diagnostics) != 1 || diagnostics[0].Code != "BASHPP-ESTRUCT-"+tc.code || !diagnostics[0].Pos.IsValid() {
				t.Fatalf("handled=%v err=%v", handled, err)
			}
			var stdout, stderr bytes.Buffer
			runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.StdIO(nil, &stdout, &stderr))
			if err != nil {
				t.Fatal(err)
			}
			status, exit := interp.IsExitStatus(runner.Run(context.Background(), file))
			if !exit || status != 2 || stdout.Len() != 0 {
				t.Fatalf("interpreter status=%d exit=%v output=%q stderr=%q", status, exit, stdout.String(), stderr.String())
			}
			expected := diagnostics[0].Code + ": " + diagnostics[0].Msg + "\n"
			if !strings.HasSuffix(stderr.String(), expected) {
				t.Fatalf("diagnostic=%q interpreter=%q", expected, stderr.String())
			}
		})
	}
}
func TestPromotedLiteralOperandOrder(t *testing.T) {
	source := `type Leaf struct { A int8; B int8 }
type Outer struct { Leaf; Middle int8 }
func main() {
 g := Outer{A: 1, Middle: 2, B: 3}
}
main()
`
	_, declared, literal := promotedFixture(t, source)
	// The positioned call node exercises native expression side effects independently
	// of the parser's currently accepted composite-call spelling.
	for i, elem := range literal.Elems {
		number := fmt.Sprint(i + 1)
		elem.Value = &syntax.BashPPCall{Fun: []*syntax.Lit{{Value: "mark"}}, Args: []*syntax.Word{{Parts: []syntax.WordPart{&syntax.Lit{Value: number}}}}}
	}
	e := typeEmitter()
	e.prefix = "pl_"
	e.funcs["mark"] = true
	expression, handled, err := e.promotedCompositeExpr(literal, declared)
	if !handled || err != nil {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
	artifact := `package main
import "fmt"
type Leaf struct {A int8;B int8};type Outer struct {Leaf;Middle int8}
var trace string
func mark(n int8) int8 {trace+=fmt.Sprint(n);return n}
func main(){g:=` + expression + `;fmt.Printf("%s:%d:%d:%d",trace,g.A,g.Middle,g.B)}
`
	if out := runPromotedArtifact(t, artifact); out != "123:1:2:3" {
		t.Fatalf("evaluation order=%q", out)
	}
}

const promotedPublicSource = "type Leaf struct { Depth int; Label string }\ntype Habitat struct { Leaf }\ntype Gopher struct { Name string; Habitat }\nfunc main() {\n g := Gopher{Name: \"x\", Depth: 3, Label: \"deep\"}\n printf '%s:%s:%s' g.Name g.Depth g.Label\n}\nmain()\n"
