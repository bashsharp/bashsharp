// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package syntax

import (
	"bytes"
	"io"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"
)

func TestBashPPCollectionASTStreamingWalkAndPrint(t *testing.T) {
	const src = "func main() {\n\ta := [3]int{1, 2: 7}\n\tb := [...]string{1: \"x\", \"y\"}\n\ts := []int{1, 2}\n\tm := map[string][]int{\"a\": {4, 5}}\n\tx := m[\"a\"][1]\n\ty := s[0:2:2]\n\tm[\"a\"][0] = 9\n}\n"
	parse := func(rd io.Reader) *File {
		f, err := NewParser(Variant(LangBashPP)).Parse(rd, "collection.bpp")
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	buffered := parse(strings.NewReader(src))
	streamed := parse(iotest.OneByteReader(strings.NewReader(src)))
	if !reflect.DeepEqual(buffered, streamed) {
		t.Fatal("buffered and one-byte trees differ")
	}
	body := buffered.Stmts[0].Cmd.(*BashPPFuncDecl).Body.Stmts
	wants := []string{"array", "inferred-array", "slice", "map"}
	for i, want := range wants {
		decl := body[i].Cmd.(*BashPPShortDecl)
		lit, ok := decl.Expr.(*BashPPCompositeLit)
		if !ok || decl.Rhs != nil {
			t.Fatalf("statement %d = %#v", i, decl)
		}
		if got := lit.LitType.(*BashPPCollectionType).Kind; got != want {
			t.Fatalf("kind %q, want %q", got, want)
		}
		if lit.Pos().Offset() >= lit.End().Offset() || !lit.Lbrace.IsValid() || !lit.Rbrace.IsValid() {
			t.Fatalf("bad positions: %#v", lit)
		}
	}
	if _, ok := body[4].Cmd.(*BashPPShortDecl).Expr.(*BashPPIndexExpr); !ok {
		t.Fatal("indexed read is not typed")
	}
	if _, ok := body[5].Cmd.(*BashPPShortDecl).Expr.(*BashPPSliceExpr); !ok {
		t.Fatal("sliced read is not typed")
	}
	assign := body[6].Cmd.(*BashPPAssign)
	if assign.TargetExpr == nil || assign.ValueExpr == nil {
		t.Fatalf("assignment is not lowered: %#v", assign)
	}
	seen := map[string]bool{}
	Walk(buffered, func(n Node) bool {
		if n != nil {
			seen[reflect.TypeOf(n).String()] = true
		}
		return true
	})
	for _, name := range []string{"*syntax.BashPPCompositeLit", "*syntax.BashPPCompositeElem", "*syntax.BashPPCollectionType", "*syntax.BashPPNamedType", "*syntax.BashPPIndexExpr", "*syntax.BashPPSliceExpr"} {
		if !seen[name] {
			t.Fatalf("Walk missed %s", name)
		}
	}
	var out bytes.Buffer
	if err := NewPrinter().Print(&out, buffered); err != nil {
		t.Fatal(err)
	}
	reparsed := parse(strings.NewReader(out.String()))
	var second bytes.Buffer
	_ = NewPrinter().Print(&second, reparsed)
	if second.String() != out.String() {
		t.Fatalf("printer is unstable:\n%s\n%s", out.String(), second.String())
	}
}

func TestBashPPCollectionDialectFallback(t *testing.T) {
	const src = "x := []int{1, 2}\n"
	for _, lang := range []LangVariant{LangBash, LangPOSIX} {
		f, err := NewParser(Variant(lang)).Parse(strings.NewReader(src), "")
		if err != nil {
			t.Fatal(err)
		}
		Walk(f, func(n Node) bool {
			if _, bad := n.(*BashPPCompositeLit); bad {
				t.Fatalf("%v produced collection AST", lang)
			}
			return true
		})
	}
}
