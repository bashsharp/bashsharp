package lower

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

func parseSharp(t *testing.T, source string) *syntax.File {
	t.Helper()
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(source), "input.bpp")
	if err != nil {
		t.Fatal(err)
	}
	return file
}
func sharpDiagnosticText(err error) string {
	var list ErrorList
	if !errors.As(err, &list) {
		if err != nil {
			return err.Error()
		}
		return ""
	}
	var out strings.Builder
	for _, d := range list {
		out.WriteString(d.Code + ": " + d.Msg + "\n")
	}
	return out.String()
}
func TestSharpCallBindingOrder(t *testing.T) {
	source := `func f(first string, second int, third int = 7) {}
f(second: 2, first: "x")
`
	file := parseSharp(t, source)
	plan, err := planSharpCall(file.Stmts[1].Cmd.(*syntax.BashPPCall), file.Stmts[0].Cmd.(*syntax.BashPPFuncDecl))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plan.ParameterOrder, []int{1, 0, 2}) {
		t.Fatal(plan.ParameterOrder)
	}
	var words []string
	for _, word := range plan.Words {
		text, ok := sharpWordText(word)
		if !ok {
			t.Fatal(word)
		}
		words = append(words, text)
	}
	if !reflect.DeepEqual(words, []string{"2", `"x"`, "7"}) {
		t.Fatal(words)
	}
}
func TestSharpStaticDiagnostics(t *testing.T) {
	cases := []struct{ source, want string }{
		{"func f(a int = 1, b int) {}\n", "BASHPP-EDEFAULT-ORDER: required parameter \"b\" follows a default parameter\n"},
		{"func f(a int = 1) {}\nf(1, 2)\n", "BASHPP-EARG-COUNT: f accepts at most 1 arguments; got 2\n"},
		{"func f(a int) {}\nf(who: 1)\n", "BASHPP-EKWARG-UNKNOWN: f has no parameter named \"who\"\n"},
		{"type Color enum { Red; Green }\nfunc f(c Color) { switch c { case Red: return } }\n", "BASHPP-EENUM-NONEXHAUSTIVE: switch on Color is missing member Green or a default arm\n"},
		{"type Color enum { Red; Green }\nc := Color(9)\n", "BASHPP-EENUM-VALUE: 9 is not a member of Color\n"},
		{"func deref(p *int) int { return *p }\necho mixed\n", "BASHPP-ENULL-DEREF: p may be nil when dereferenced\n"},
		{"func deref(p *int, replacement *int) int { if p == nil { return 0 }; p = replacement; return *p }\n", "BASHPP-ENULL-DEREF: p may be nil after reassignment\n"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			err := CheckBashSharp(parseSharp(t, tc.source))
			if got := sharpDiagnosticText(err); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
			var list ErrorList
			if errors.As(err, &list) {
				for _, d := range list {
					if !d.Pos.IsValid() {
						t.Fatal("missing source position")
					}
				}
			}
		})
	}
}
func TestSharpNullGuardsAndMixedSource(t *testing.T) {
	source := `echo before
func positive(p *int) bool { return p != nil && *p > 0 }
func first(xs []string) string {
 if xs == nil { return "" }
 return xs[0]
}
func deref(p *int) int { if p == nil { return 0 }; return *p }
echo after
`
	if err := CheckBashSharp(parseSharp(t, source)); err != nil {
		t.Fatal(err)
	}
}

// Run with BASHSHARP_CORPUS pointing at the public tests/bashsharp directory.
// Paths are runtime configuration; neither fixture source nor a checkout path
// is embedded in generated artifacts. This verifies static acceptance/rejection
// phase and exact public diagnostics, not compiled runtime parity.
func TestBashSharpPublicCorpus(t *testing.T) {
	root := os.Getenv("BASHSHARP_CORPUS")
	if root == "" {
		t.Skip("set BASHSHARP_CORPUS to run the public 33-case static-phase contract")
	}
	ledgers, err := filepath.Glob(filepath.Join(root, "*", "lowering.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	total, rejected := 0, 0
	for _, ledger := range ledgers {
		contents, err := os.ReadFile(ledger)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(contents), "\n") {
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			cells := strings.Split(line, "\t")
			if len(cells) != 6 {
				t.Fatal(line)
			}
			total++
			directory := filepath.Dir(ledger)
			t.Run(filepath.Base(directory)+"/"+cells[0], func(t *testing.T) {
				input, err := os.ReadFile(filepath.Join(directory, cells[1]))
				if err != nil {
					t.Fatal(err)
				}
				observed := sharpDiagnosticText(CheckBashSharp(parseSharp(t, string(input))))
				want := ""
				if cells[2] == "reject" {
					rejected++
					data, err := os.ReadFile(filepath.Join(directory, cells[5]))
					if err != nil {
						t.Fatal(err)
					}
					want = string(data)
				}
				if observed != want {
					t.Fatalf("static diagnostic=%q want=%q", observed, want)
				}
			})
		}
	}
	if total != 33 || rejected != 15 {
		t.Fatalf("unexpected corpus %d total, %d rejects", total, rejected)
	}
}
