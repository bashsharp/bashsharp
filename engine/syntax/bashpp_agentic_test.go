package syntax_test

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/bashsharp/bashsharp/engine/syntax/typedjson"
)

func TestBashPPAgentic(t *testing.T) {
	for _, src := range []string{
		"agentic func f(n int) int { return n }",
		"agentic func f[T any](n T) T { return n }",
		"agentic func (r Report) Summarize() string { return r.Text }",
		"agentic function f() { echo ok; }",
		"agentic { echo ok; }",
		"agentic {\nagentic { echo ok; }\n}",
	} {
		t.Run(src, func(t *testing.T) {
			for _, reader := range []io.Reader{strings.NewReader(src), iotest.OneByteReader(strings.NewReader(src))} {
				f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(reader, "agentic.bpp")
				if err != nil {
					t.Fatal(err)
				}
				if f.Stmts[0].Cmd.Pos().Offset() != 0 {
					t.Fatal("modifier position lost")
				}
				seen := false
				syntax.Walk(f, func(n syntax.Node) bool {
					if lit, ok := n.(*syntax.Lit); ok && lit.Value == "agentic" {
						seen = true
					}
					return true
				})
				if !seen {
					t.Fatal("Walk omitted modifier")
				}
				var encoded bytes.Buffer
				if err := typedjson.Encode(&encoded, f); err != nil {
					t.Fatal(err)
				}
				n, err := typedjson.Decode(&encoded)
				if err != nil {
					t.Fatal(err)
				}
				var printed bytes.Buffer
				if err := syntax.NewPrinter().Print(&printed, n); err != nil {
					t.Fatal(err)
				}
				if !strings.HasPrefix(printed.String(), "agentic ") {
					t.Fatalf("modifier lost: %s", &printed)
				}
				if _, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(&printed, ""); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestBashPPAgenticFallback(t *testing.T) {
	for _, src := range []string{"agentic", "agentic word", "agentic func f", "agentic function f", "command agentic {", `"agentic" {`, `agentic "{"`, "echo agentic {", "agentic {word"} {
		for _, lang := range []syntax.LangVariant{syntax.LangBashPP, syntax.LangBash, syntax.LangPOSIX} {
			f, err := syntax.NewParser(syntax.Variant(lang)).Parse(iotest.OneByteReader(strings.NewReader(src)), "")
			if err != nil {
				t.Fatalf("%s (%s): %v", src, lang, err)
			}
			if _, ok := f.Stmts[0].Cmd.(*syntax.CallExpr); !ok {
				t.Fatalf("%s: stole classic command", src)
			}
		}
	}
	for _, src := range []string{"agentic(2) func f() {}", "agentic func f(2) {}", "agentic function f(2) {}", "agentic {", `agentic function "f"() { :; }`, `agentic function $f() { :; }`, "agentic func (bad) M() {}", "agentic func f() {", "agentic function f() {"} {
		if _, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), ""); err == nil {
			t.Fatalf("accepted %s", src)
		}
	}
}

func TestBashPPAgenticFunctionAliasSyntax(t *testing.T) {
	for _, src := range []string{
		"func f() {}\nx := f\nx()\n",
		"agentic func f() {}\nx := f\ny := x\nagentic { y(); }",
		"func f() {}\nx := f\nx() { echo shell; }\nx",
	} {
		if _, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), ""); err != nil {
			t.Fatal(err)
		}
	}
}
