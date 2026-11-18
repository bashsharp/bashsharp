package typedjson_test

import (
	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/bashsharp/bashsharp/engine/syntax/typedjson"
	"reflect"
	"strings"
	"testing"
)

func TestBashPPTypedOperandsRoundTrip(t *testing.T) {
	src := "func positive(p *int) bool { return p != nil && *p > 0 }\nfunc f() {\n x := (1 + 2)\n s := `raw`\n}\n"
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "operands.bpp")
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := typedjson.Encode(&out, f); err != nil {
		t.Fatal(err)
	}
	restored, err := typedjson.Decode(strings.NewReader(out.String()))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f, restored) {
		t.Fatal("typed JSON changed scalar return/raw/parenthesized AST")
	}
	r := restored.(*syntax.File).Stmts[0].Cmd.(*syntax.BashPPFuncDecl).Body.Stmts[0].Cmd.(*syntax.BashPPReturn)
	if r.Expr == nil {
		t.Fatal("missing Return.Expr")
	}
}
