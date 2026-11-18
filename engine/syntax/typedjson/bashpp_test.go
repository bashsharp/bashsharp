// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package typedjson_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/bashsharp/bashsharp/engine/syntax/typedjson"
)

// TestBashPPDeclRoundTrip covers the Bash++ nodes on the JSON surface, which
// wiring the parser dispatch made reachable for the first time.
//
// It is here because the reachability itself was the bug. Encoding is
// reflective and needs no registration, so a Bash++ node reached the encoder
// the moment LangBashPP could produce one — and turned up two failures that
// only a tree containing one can show:
//
//   - a node field named Type collides with the "Type" discriminator this
//     package puts first in every tagged object, and panics reflect.StructOf.
//     That is why [syntax.BashPPDecl] spells its field DeclType.
//   - this package encodes small enums by their string form so the wire format
//     survives new values, and decodes them through TextUnmarshaler — which
//     had only ever been needed for uint32 tokens, so a uint8 enum encoded
//     cleanly and then panicked on the way back in.
//
// Both are invisible to a unit test that builds a node directly and never
// serializes it, and both would have surfaced as a `shfmt --tojson` panic.
func TestBashPPDeclRoundTrip(t *testing.T) {
	t.Parallel()

	for _, src := range []string{
		"var x = 1\n",
		"const K = 2\n",
		"type T int\n",
		"type ID = string\n",
		"echo before\nvar x = 1\necho after\n",
		"if true; then var x = 1; fi\n",
	} {
		t.Run(src, func(t *testing.T) {
			t.Parallel()
			parser := syntax.NewParser(syntax.Variant(syntax.LangBashPP))
			f, err := parser.Parse(strings.NewReader(src), "")
			if err != nil {
				t.Fatal(err)
			}

			var enc strings.Builder
			if err := typedjson.Encode(&enc, f); err != nil {
				t.Fatalf("encoding: %v", err)
			}
			if !strings.Contains(enc.String(), `"Type":"BashPPDecl"`) {
				t.Fatalf("encoded form does not carry the node's type tag:\n%s", enc.String())
			}

			node, err := typedjson.Decode(strings.NewReader(enc.String()))
			if err != nil {
				t.Fatalf("decoding: %v", err)
			}
			var out strings.Builder
			if err := syntax.NewPrinter().Print(&out, node); err != nil {
				t.Fatalf("printing the decoded tree: %v", err)
			}
			if out.String() != src {
				t.Fatalf("round trip produced %q, want %q", out.String(), src)
			}
		})
	}
}

func TestBashPPFormsRoundTrip(t *testing.T) {
	for _, src := range []string{"x := 42\n", "x, y := 1, 2\n", "func main() {\n\tx := 1 + 2\n}\n", "f(1, 2)\n", "x.y.z()\n", "import f \"fmt\"\n", "import (\n\tf \"fmt\"\n\t_ \"embed\"\n)\n", "type Config struct { Name string; Ports []int }\n", "type Color enum { Red; Green }\nfunc label(c Color) string {\n\tswitch c {\n\tcase Red:\n\t\treturn \"red\"\n\tcase Green:\n\t\treturn \"green\"\n\t}\n}\n", `cfg["ports"][0] = 8080` + "\n"} {
		t.Run(src, func(t *testing.T) {
			f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "")
			if err != nil {
				t.Fatal(err)
			}
			var enc strings.Builder
			if err := typedjson.Encode(&enc, f); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(src, "1 + 2") && !strings.Contains(enc.String(), `"GoRegion":true`) {
				t.Fatalf("encoded form does not carry committed Go-region provenance:\n%s", enc.String())
			}
			node, err := typedjson.Decode(strings.NewReader(enc.String()))
			if err != nil {
				t.Fatal(err)
			}
			var out strings.Builder
			if err := syntax.NewPrinter().Print(&out, node); err != nil || out.String() != src {
				t.Fatalf("print %q: %v", out.String(), err)
			}
		})
	}
}

func TestBashPPScalarExpressionTypedJSONRoundTrip(t *testing.T) {
	// The last two are spelled with shell metacharacters, so they reach the
	// typed tree only through the carrier in syntax/bashpp_scalar.go; encoding
	// and printing must treat them like any other operator.
	const src = "func main() {\n\tbase := 1\n\tneg := -base\n\tsum := base + 2\n" +
		"\ttext := string(1)\n\tcmp := base < 2\n\tmask := base &^ 2\n}\n"
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "expr.bpp")
	if err != nil {
		t.Fatal(err)
	}
	body := f.Stmts[0].Cmd.(*syntax.BashPPFuncDecl).Body
	for i, stmt := range body.Stmts {
		decl := stmt.Cmd.(*syntax.BashPPShortDecl)
		if decl.Expr == nil || decl.Rhs != nil {
			t.Fatalf("short declaration %d did not retain a typed scalar expression: %#v", i, decl)
		}
	}
	var seen []string
	syntax.Walk(f, func(n syntax.Node) bool {
		seen = append(seen, fmt.Sprintf("%T", n))
		return true
	})
	for _, typeName := range []string{
		"*syntax.BashPPBasicLit", "*syntax.BashPPIdent", "*syntax.BashPPUnaryExpr",
		"*syntax.BashPPBinaryExpr", "*syntax.BashPPConvertExpr",
	} {
		if !slices.Contains(seen, typeName) {
			t.Fatalf("Walk missed %s in typed expression nodes: %v", typeName, seen)
		}
	}
	var enc strings.Builder
	if err := typedjson.Encode(&enc, f); err != nil {
		t.Fatal(err)
	}
	for _, typeName := range []string{
		"BashPPBasicLit", "BashPPIdent", "BashPPUnaryExpr", "BashPPBinaryExpr", "BashPPConvertExpr",
	} {
		if !strings.Contains(enc.String(), `"Type":"`+typeName+`"`) {
			t.Fatalf("typed expression node %s missing from JSON: %s", typeName, enc.String())
		}
	}
	node, err := typedjson.Decode(strings.NewReader(enc.String()))
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := syntax.NewPrinter().Print(&out, node); err != nil || out.String() != src {
		t.Fatalf("decoded tree printed %q (%v), want %q", out.String(), err, src)
	}
}

func TestBashPPBuiltinResidualTypedJSONRoundTrip(t *testing.T) {
	const src = "func main() {\n\tbuf := make([]byte, 0, 4)\n\tbuf = append(buf, \"x\"...)\n\tn := copy(buf, \"y\")\n\tm := max(9007199254740992, 9007199254740993)\n}\n"
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "builtin-residual.bpp")
	if err != nil {
		t.Fatal(err)
	}
	var enc strings.Builder
	if err := typedjson.Encode(&enc, f); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(enc.String(), `"Ellipsis":{"Offset":`) || !strings.Contains(enc.String(), `"Value":"byte"`) {
		t.Fatalf("encoded builtin calls lost spread/type evidence: %s", enc.String())
	}
	node, err := typedjson.Decode(strings.NewReader(enc.String()))
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := syntax.NewPrinter().Print(&out, node); err != nil || out.String() != src {
		t.Fatalf("decoded builtin tree printed %q (%v), want %q", out.String(), err, src)
	}
}
