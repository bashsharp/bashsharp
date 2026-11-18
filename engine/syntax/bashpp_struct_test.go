// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package syntax

import (
	"io"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"
)

func TestBashPPStructASTStreamingWalkAndPrint(t *testing.T) {
	const src = `type Inner struct { Name string }
type Config struct { Inner Inner; Fixed [1]Inner; Ports []int }
func main() {
 cfg := Config{Inner: Inner{Name: "prod"}, Fixed: [1]Inner{{Name: "fixed"}}}
 anon := struct{Name string}{Name: "x"}
 name := cfg.Inner.Name
 cfg.Fixed[0].Name = "changed"
}
`
	parse := func(rd io.Reader) *File {
		f, err := NewParser(Variant(LangBashPP)).Parse(rd, "struct.bpp")
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	buffered := parse(strings.NewReader(src))
	streamed := parse(iotest.OneByteReader(strings.NewReader(src)))
	if !reflect.DeepEqual(buffered, streamed) {
		t.Fatal("buffered and one-byte struct trees differ")
	}
	decl := buffered.Stmts[1].Cmd.(*BashPPDecl)
	if _, ok := decl.DeclTypeExpr.(*BashPPStructType); !ok {
		t.Fatalf("declaration type = %T", decl.DeclTypeExpr)
	}
	for _, field := range decl.StructFields {
		if field.FieldTypeExpr == nil || !field.FieldTypeExpr.Pos().IsValid() || !field.FieldTypeExpr.End().IsValid() {
			t.Fatalf("field type is not positioned: %#v", field)
		}
	}
	body := buffered.Stmts[2].Cmd.(*BashPPFuncDecl).Body.Stmts
	if _, ok := body[0].Cmd.(*BashPPShortDecl).Expr.(*BashPPCompositeLit).LitType.(*BashPPNamedType); !ok {
		t.Fatal("named struct literal is not typed")
	}
	anonDecl, ok := body[1].Cmd.(*BashPPShortDecl)
	if !ok {
		var kinds []string
		for _, stmt := range body {
			kinds = append(kinds, reflect.TypeOf(stmt.Cmd).String())
		}
		t.Fatalf("anonymous declaration = %T; body commands: %v", body[1].Cmd, kinds)
	}
	anon, ok := anonDecl.Expr.(*BashPPCompositeLit)
	if !ok {
		t.Fatalf("anonymous expression = %T", anonDecl.Expr)
	}
	if _, ok := anon.LitType.(*BashPPStructType); !ok {
		t.Fatalf("anonymous struct type = %T", anon.LitType)
	}
	if _, ok := body[2].Cmd.(*BashPPShortDecl).Expr.(*BashPPSelectorExpr); !ok {
		t.Fatal("selector read is not typed")
	}
	if _, ok := body[3].Cmd.(*BashPPAssign).TargetExpr.(*BashPPSelectorExpr); !ok {
		t.Fatal("selector assignment is not typed")
	}
	seen := map[string]bool{}
	Walk(buffered, func(n Node) bool {
		if n != nil {
			seen[reflect.TypeOf(n).String()] = true
		}
		return true
	})
	for _, want := range []string{"*syntax.BashPPStructType", "*syntax.BashPPSelectorExpr"} {
		if !seen[want] {
			t.Fatalf("Walk missed %s", want)
		}
	}
	var first strings.Builder
	if err := NewPrinter().Print(&first, buffered); err != nil {
		t.Fatal(err)
	}
	reparsed := parse(strings.NewReader(first.String()))
	var second strings.Builder
	_ = NewPrinter().Print(&second, reparsed)
	if first.String() != second.String() {
		t.Fatalf("printer is unstable:\n%s\n%s", first.String(), second.String())
	}
}

func TestBashPPStructDialectAndTopLevelFallback(t *testing.T) {
	const src = "x := Config{Name: \"x\"}\ny := struct{Name string}{Name: \"y\"}\n"
	for _, lang := range []LangVariant{LangBash, LangPOSIX} {
		f, err := NewParser(Variant(lang)).Parse(strings.NewReader(src), "")
		if err != nil {
			t.Fatal(err)
		}
		Walk(f, func(n Node) bool {
			switch n.(type) {
			case *BashPPStructType, *BashPPSelectorExpr, *BashPPCompositeLit:
				t.Fatalf("%v produced typed struct AST", lang)
			}
			return true
		})
	}
	// A top-level Class-E near miss remains the exact shell tree Bash sees.
	bashppCheckDiagnostic(t, "x := Config{Name: nope extra}\n", "invalid := binding")
}
