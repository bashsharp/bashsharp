// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package syntax

import (
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

func TestBashPPRangeScalarOperandsRoundTrip(t *testing.T) {
	const src = "func f() {\n\tfor i, r := range \"aé\" { }\n\tfor i := range 3 { }\n\tfor range n + 1 { }\n}\n"
	f, err := NewParser(Variant(LangBashPP)).Parse(strings.NewReader(src), "range.bpp")
	if err != nil {
		t.Fatal(err)
	}
	body := f.Stmts[0].Cmd.(*BashPPFuncDecl).Body.Stmts
	for i, stmt := range body {
		rng, ok := stmt.Cmd.(*BashPPRange)
		if !ok || rng.Expr == nil || rng.Range.Line() != uint(i+2) {
			t.Fatalf("statement %d range = %#v", i, stmt.Cmd)
		}
	}
	if _, ok := body[0].Cmd.(*BashPPRange).Expr.(*BashPPBasicLit); !ok {
		t.Fatalf("string operand = %T", body[0].Cmd.(*BashPPRange).Expr)
	}
	if _, ok := body[1].Cmd.(*BashPPRange).Expr.(*BashPPBasicLit); !ok {
		t.Fatalf("integer operand = %T", body[1].Cmd.(*BashPPRange).Expr)
	}
	if _, ok := body[2].Cmd.(*BashPPRange).Expr.(*BashPPBinaryExpr); !ok {
		t.Fatalf("expression operand = %T", body[2].Cmd.(*BashPPRange).Expr)
	}
	var out strings.Builder
	if err := NewPrinter().Print(&out, f); err != nil {
		t.Fatal(err)
	}
	if out.String() != src {
		t.Fatalf("round trip = %q, want %q", out.String(), src)
	}
}

func TestBashPPRangeInvalidArityPositionedDiagnostic(t *testing.T) {
	const src = "func f() {\n\tfor a, b, c := range values { }\n}\n"
	parse := func(rd io.Reader) string {
		_, err := NewParser(Variant(LangBashPP)).Parse(rd, "range.bpp")
		if err == nil {
			t.Fatal("expected range arity diagnostic")
		}
		return err.Error()
	}
	buffered := parse(strings.NewReader(src))
	streamed := parse(iotest.OneByteReader(strings.NewReader(src)))
	if buffered != streamed {
		t.Fatalf("buffered diagnostic %q != streamed %q", buffered, streamed)
	}
	if !strings.Contains(buffered, "range.bpp:2:12: bash++ range permits at most two iteration variables") {
		t.Fatalf("diagnostic = %q", buffered)
	}
}

func TestBashPPRangeScalarClassicIsolation(t *testing.T) {
	const src = "func f() {\n\tfor i := range 3 {}\n}\n"
	for _, lang := range []LangVariant{LangBash, LangPOSIX} {
		if _, err := NewParser(Variant(lang)).Parse(strings.NewReader(src), "range.bpp"); err == nil {
			t.Fatalf("%v accepted Bash++ range", lang)
		}
	}
}
