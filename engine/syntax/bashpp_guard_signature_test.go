// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.
package syntax

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

// These sources retain the required public Bash# null-safety fixture spellings.
const guardSignatureSource = `func positive(p *int) bool {
    return p != nil && *p > 0
}
func first(xs []string) string {
    if xs == nil || len(xs) == 0 {
        return ""
    }
    return xs[0]
}
func invoke(fn func() int) int {
    return fn()
}
`

type guardByteReader struct{ io.Reader }

func (r guardByteReader) Read(p []byte) (int, error) {
	if len(p) > 1 {
		p = p[:1]
	}
	return r.Reader.Read(p)
}

func TestBashPPGuardAndConcreteSignature(t *testing.T) {
	for _, bytewise := range []bool{false, true} {
		var input io.Reader = strings.NewReader(guardSignatureSource)
		if bytewise {
			input = guardByteReader{input}
		}
		file, err := NewParser(Variant(LangBashPP)).Parse(input, "guards.bpp")
		if err != nil {
			t.Fatal(err)
		}
		first := file.Stmts[1].Cmd.(*BashPPFuncDecl)
		guard := first.Body.Stmts[0].Cmd.(*BashPPIf)
		disjunction := guard.Cond.(*BashPPBinaryExpr)
		comparison := disjunction.Y.(*BashPPBinaryExpr)
		call, ok := comparison.X.(*BashPPCall)
		if !ok || call.Fun[0].Value != "len" || len(call.Args) != 1 || call.Args[0].Lit() != "xs" {
			t.Fatalf("nested call: %#v", comparison.X)
		}
		if call.Pos().Line() != 5 || call.Pos().Col() != 21 || call.End().Col() != 28 {
			t.Fatalf("call positions: %v..%v", call.Pos(), call.End())
		}
		invoke := file.Stmts[2].Cmd.(*BashPPFuncDecl)
		typ, ok := invoke.Params[0].FieldTypeExpr.(*BashPPFuncType)
		if !ok || len(typ.Params) != 0 || len(typ.Results) != 1 || typ.Results[0].FieldType.Value != "int" {
			t.Fatalf("signature: %#v", invoke.Params[0])
		}
		if invoke.Params[0].FieldType.Value != "func() int" || typ.Pos().Line() != 10 || typ.Pos().Col() != 16 || typ.End().Col() != 26 {
			t.Fatalf("type positions/text: %#v", invoke.Params[0])
		}
		ret := invoke.Body.Stmts[0].Cmd.(*BashPPReturn)
		if ret.Call == nil || ret.Call.Fun[0].Value != "fn" {
			t.Fatalf("callback return: %#v", ret)
		}
		seenType, seenCall := false, false
		Walk(file, func(n Node) bool {
			switch n.(type) {
			case *BashPPFuncType:
				seenType = true
			case *BashPPCall:
				seenCall = true
			}
			return true
		})
		if !seenType || !seenCall {
			t.Fatal("walk omitted signature/call")
		}
		var printed bytes.Buffer
		if err := NewPrinter().Print(&printed, file); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(printed.String(), "if xs == nil || len(xs) == 0") || !strings.Contains(printed.String(), "fn func() int") {
			t.Fatalf("printed %s", printed.String())
		}
		reparsed, err := NewParser(Variant(LangBashPP)).Parse(strings.NewReader(printed.String()), "guards.bpp")
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := reparsed.Stmts[2].Cmd.(*BashPPFuncDecl).Params[0].FieldTypeExpr.(*BashPPFuncType); !ok {
			t.Fatal("print/reparse lost concrete signature")
		}
	}
}

func TestBashPPGuardSignatureDialectOwnership(t *testing.T) {
	for _, lang := range []LangVariant{LangBash, LangPOSIX} {
		for _, bytewise := range []bool{false, true} {
			var input io.Reader = strings.NewReader(guardSignatureSource)
			if bytewise {
				input = guardByteReader{input}
			}
			if _, err := NewParser(Variant(lang)).Parse(input, ""); err == nil {
				t.Fatalf("classic dialect %v accepted typed functions", lang)
			}
		}
	}
	// The enclosing if transaction must roll back even after reading a call-like
	// condition fragment; legal shell grouping and then retain the shell node.
	for _, src := range []string{"if (true); then echo yes; fi\n", "if true; then echo 'len(xs)'; fi\n"} {
		for _, lang := range []LangVariant{LangBashPP, LangBash, LangPOSIX} {
			f, err := NewParser(Variant(lang)).Parse(guardByteReader{strings.NewReader(src)}, "")
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := f.Stmts[0].Cmd.(*IfClause); !ok {
				t.Fatalf("shell if claimed: %T", f.Stmts[0].Cmd)
			}
		}
	}
	// A concrete local callback name must not classify a later command-position
	// fn() as a typed call; the classic shell requires a function body there.
	src := "func invoke(fn func() int) int {\n return fn()\n}\nfn()\n"
	if _, err := NewParser(Variant(LangBashPP)).Parse(strings.NewReader(src), ""); err == nil {
		t.Fatal("local callable leaked beyond its function")
	}
}

func TestBashPPGuardSignatureMalformedPositioned(t *testing.T) {
	for _, src := range []string{
		"func f(xs []int) {\n if len(xs == 0 { echo bad }\n}\n",
		"func invoke(fn func( int) {\n return fn()\n}\n",
	} {
		for _, bytewise := range []bool{false, true} {
			var input io.Reader = strings.NewReader(src)
			if bytewise {
				input = guardByteReader{input}
			}
			_, err := NewParser(Variant(LangBashPP)).Parse(input, "malformed.bpp")
			parseErr, ok := err.(ParseError)
			if !ok || !parseErr.Pos.IsValid() {
				t.Fatalf("want positioned parse error, got %T: %v", err, err)
			}
		}
	}
}
