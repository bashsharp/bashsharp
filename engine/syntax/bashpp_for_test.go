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

func parseBashPPFor(t *testing.T, rd io.Reader) *File {
	t.Helper()
	f, err := NewParser(Variant(LangBashPP)).Parse(rd, "for.bpp")
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestBashPPForFormsPositionsWalkAndStreaming(t *testing.T) {
	const src = "func f() {\n\tfor { }\n\tfor n < 3 {\n\t\techo $n\n\t}\n\tfor i := 0; i < 3; i++ {\n\t\techo $i\n\t}\n}\n"
	buffered := parseBashPPFor(t, strings.NewReader(src))
	streamed := parseBashPPFor(t, iotest.OneByteReader(strings.NewReader(src)))
	if !reflect.DeepEqual(buffered, streamed) {
		t.Fatalf("buffered and one-byte trees differ:\n%#v\n%#v", buffered, streamed)
	}
	body := buffered.Stmts[0].Cmd.(*BashPPFuncDecl).Body
	inf := body.Stmts[0].Cmd.(*BashPPFor)
	cond := body.Stmts[1].Cmd.(*BashPPFor)
	clause := body.Stmts[2].Cmd.(*BashPPFor)
	if inf.Init != nil || inf.Cond != nil || inf.Post != nil || inf.FirstSemi.IsValid() || inf.SecondSemi.IsValid() {
		t.Fatalf("infinite loop has a clause: %#v", inf)
	}
	if cond.Cond == nil || cond.Init != nil || cond.Post != nil {
		t.Fatalf("condition loop = %#v", cond)
	}
	if _, ok := clause.Init.(*BashPPShortDecl); !ok {
		t.Fatalf("init = %T, want *BashPPShortDecl", clause.Init)
	}
	if _, ok := clause.Post.(*BashPPIncDec); !ok {
		t.Fatalf("post = %T, want *BashPPIncDec", clause.Post)
	}
	for label, got := range map[string]Pos{
		"for": clause.For, "first semi": clause.FirstSemi, "second semi": clause.SecondSemi,
		"body open": clause.Body.Lbrace, "body close": clause.Body.Rbrace,
	} {
		needle := map[string]string{
			"for": "for i :=", "first semi": "; i <", "second semi": "; i++",
			"body open": "{\n\t\techo $i", "body close": "\t}\n}\n",
		}[label]
		off := strings.LastIndex(src, needle)
		if label == "body close" {
			off++
		}
		if off < 0 || got.Offset() != uint(off) {
			t.Errorf("%s offset = %d, want %d", label, got.Offset(), off)
		}
	}
	var seen []string
	Walk(clause, func(n Node) bool {
		if n != nil {
			seen = append(seen, reflect.TypeOf(n).String())
		}
		return true
	})
	for _, want := range []string{"*syntax.BashPPFor", "*syntax.BashPPShortDecl", "*syntax.BashPPBinaryExpr", "*syntax.BashPPIncDec", "*syntax.Block"} {
		if !slicesContains(seen, want) {
			t.Errorf("Walk omitted %s: %v", want, seen)
		}
	}
	var printed bytes.Buffer
	if err := NewPrinter().Print(&printed, buffered); err != nil {
		t.Fatal(err)
	}
	if printed.String() != src {
		t.Fatalf("print = %q, want %q", printed.String(), src)
	}
	reparsed := parseBashPPFor(t, strings.NewReader(printed.String()))
	if !reflect.DeepEqual(buffered, reparsed) {
		t.Fatal("parse/print/reparse changed tree")
	}
}

func slicesContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestBashPPForScalarInitAndPostStatements(t *testing.T) {
	const src = "func f() {\n\tfor i = 1; i < 4; i = i + 1 {}\n\tfor i++; i < 4; i-- {}\n}\n"
	f := parseBashPPFor(t, strings.NewReader(src))
	body := f.Stmts[0].Cmd.(*BashPPFuncDecl).Body
	first := body.Stmts[0].Cmd.(*BashPPFor)
	if _, ok := first.Init.(*BashPPForAssign); !ok {
		t.Fatalf("assignment init = %T", first.Init)
	}
	if _, ok := first.Post.(*BashPPForAssign); !ok {
		t.Fatalf("assignment post = %T", first.Post)
	}
	second := body.Stmts[1].Cmd.(*BashPPFor)
	if _, ok := second.Init.(*BashPPIncDec); !ok {
		t.Fatalf("inc-dec init = %T", second.Init)
	}
	if post, ok := second.Post.(*BashPPIncDec); !ok || post.Op.Value != "--" {
		t.Fatalf("inc-dec post = %#v", second.Post)
	}
}

func TestBashPPForPreservesOtherForForms(t *testing.T) {
	const src = "func f() {\n\tfor x in a b; do echo $x; done\n\tfor x; { echo $x; }\n\tfor ((i=0; i<2; i++)); do echo $i; done\n\tfor v := range ch {}\n}\n"
	f := parseBashPPFor(t, strings.NewReader(src))
	body := f.Stmts[0].Cmd.(*BashPPFuncDecl).Body
	if _, ok := body.Stmts[0].Cmd.(*ForClause); !ok {
		t.Fatalf("classic for = %T", body.Stmts[0].Cmd)
	}
	if braced, ok := body.Stmts[1].Cmd.(*ForClause); !ok || !braced.Braces {
		t.Fatalf("braced classic for = %#v", body.Stmts[1].Cmd)
	}
	if arith, ok := body.Stmts[2].Cmd.(*ForClause); !ok {
		t.Fatalf("arithmetic for = %T", body.Stmts[2].Cmd)
	} else if _, ok := arith.Loop.(*CStyleLoop); !ok {
		t.Fatalf("arithmetic loop = %T", arith.Loop)
	}
	if _, ok := body.Stmts[3].Cmd.(*BashPPRange); !ok {
		t.Fatalf("range = %T", body.Stmts[3].Cmd)
	}

	typed := "for i := 0; i < 1; i++ {}\n"
	for _, lang := range []LangVariant{LangBashPP, LangBash, LangPOSIX} {
		file, _ := NewParser(Variant(lang)).Parse(strings.NewReader(typed), "")
		if file != nil {
			Walk(file, func(n Node) bool {
				if _, ok := n.(*BashPPFor); ok {
					t.Fatalf("top-level %v produced BashPPFor", lang)
				}
				return true
			})
		}
	}
}

func TestBashPPForStableDiagnostics(t *testing.T) {
	const src = "func f() {\n\tfor i := 0; i < 2; j := 1 {}\n}\n"
	parseErr := func(rd io.Reader) string {
		_, err := NewParser(Variant(LangBashPP)).Parse(rd, "for.bpp")
		if err == nil {
			t.Fatal("expected parse error")
		}
		return err.Error()
	}
	buffered := parseErr(strings.NewReader(src))
	streamed := parseErr(iotest.OneByteReader(strings.NewReader(src)))
	if buffered != streamed {
		t.Fatalf("diagnostics differ: %q != %q", buffered, streamed)
	}
	if !strings.Contains(buffered, "bash++ for post must be a scalar assignment or inc-dec statement") {
		t.Fatalf("diagnostic = %q", buffered)
	}
}

func TestBashPPForAddsLoopControlStatements(t *testing.T) {
	const src = "func f() {\n\tfor false {\n\t\tbreak\n\t\tcontinue\n\t}\n}\n"
	f := parseBashPPFor(t, strings.NewReader(src))
	loop := f.Stmts[0].Cmd.(*BashPPFuncDecl).Body.Stmts[0].Cmd.(*BashPPFor)
	for i, stmt := range loop.Body.Stmts {
		if _, ok := stmt.Cmd.(*BashPPBranch); !ok {
			t.Fatalf("body statement %d = %T, want *BashPPBranch", i, stmt.Cmd)
		}
	}
}
