package lower

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestReturnStatementConversionTypeGrouping(t *testing.T) {
	// Typechecking distinguishes a pointer conversion from dereferencing a
	// conversion to its element type; text matching alone would miss this bug.
	for _, typ := range []string{"*Box", "**Box", "[]*Box", "map[string]*Box", "func() *Box", "Reader"} {
		t.Run(typ, func(t *testing.T) {
			e := &emitter{scopes: []map[string]bool{{"value": true}}, resultTypes: []string{typ}}
			n := &syntax.BashPPReturn{Results: []*syntax.Word{{Parts: []syntax.WordPart{&syntax.Lit{Value: "value"}}}}}
			statement, err := e.returnStatement(n)
			if err != nil {
				t.Fatal(err)
			}
			source := "package p\ntype Box struct { N int }\ntype Reader interface { Read() }\nfunc result(value " + typ + ") " + typ + " { " + statement + " }\n"
			fs := token.NewFileSet()
			file, err := parser.ParseFile(fs, "result.go", source, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := (&types.Config{}).Check("p", fs, []*ast.File{file}, nil); err != nil {
				t.Fatalf("invalid result conversion: %v\n%s", err, source)
			}
		})
	}
}

func TestReturnTypesUsesPositionedType(t *testing.T) {
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader("func rich() (*Box, *Box) { return p, q }\n"), "result.bpp")
	if err != nil {
		t.Fatal(err)
	}
	decl := file.Stmts[0].Cmd.(*syntax.BashPPFuncDecl)
	// Positioned-only producers must not need a legacy text copy. Keep this
	// independent from whether today's parser supplies both representations.
	for _, field := range decl.Results {
		field.FieldType = nil
		field.FieldTypeExpr = &syntax.BashPPPointerType{Element: &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "Box"}}}
	}
	got := (&emitter{}).returnTypes(decl.Results)
	if len(got) != 2 || got[0] != "*Box" || got[1] != "*Box" {
		t.Fatalf("positioned result types: %v", got)
	}
}
