// Copyright (c) 2026, the bash++ authors.
// See LICENSE for licensing information.

package typedjson

import (
	"bytes"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// TestBashPPChanRoundTrip pushes every P4 channel node through the typed JSON
// surface and back. It matters more than it looks: the encoder builds its
// structs by reflection over the node types, so a field the parser populates
// but the registry does not know about is dropped SILENTLY — the tree still
// decodes, just without the part that carried the meaning. Decoding and then
// reprinting is what turns that into a failure.
func TestBashPPChanRoundTrip(t *testing.T) {
	const src = "func f() {\n" +
		"\tch := make(chan int, 2)\n" +
		"\tgo worker(1)\n" +
		"\tch <- 1\n" +
		"\tv, ok := <-ch\n" +
		"\t<-ch\n" +
		"\tclose(ch)\n" +
		"\tfor x := range ch {\n" +
		"\t\techo $x\n" +
		"\t}\n" +
		"\tselect {\n" +
		"\tcase y := <-ch:\n" +
		"\t\techo $y\n" +
		"\tcase ch <- 2:\n" +
		"\t\techo sent\n" +
		"\tdefault:\n" +
		"\t\techo none\n" +
		"\t}\n" +
		"}\n"
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "")
	if err != nil {
		t.Fatal(err)
	}
	var data bytes.Buffer
	if err := (EncodeOptions{Indent: "\t"}).Encode(&data, f); err != nil {
		t.Fatal(err)
	}
	n, err := Decode(bytes.NewReader(data.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := syntax.NewPrinter().Print(&out, n.(*syntax.File)); err != nil {
		t.Fatal(err)
	}
	if out.String() != src {
		t.Fatalf("print after JSON round trip:\n got %q\nwant %q", out.String(), src)
	}

	body := n.(*syntax.File).Stmts[0].Cmd.(*syntax.BashPPFuncDecl).Body.Stmts
	mk := body[0].Cmd.(*syntax.BashPPShortDecl).MakeChan
	if mk == nil || mk.ChanType == nil || mk.ChanType.Elem.Value != "int" || mk.Capacity.Lit() != "2" {
		t.Fatalf("make(chan …) after JSON round trip = %#v", mk)
	}
	if _, ok := body[1].Cmd.(*syntax.BashPPGo); !ok {
		t.Fatalf("go call after JSON round trip = %T", body[1].Cmd)
	}
	if send := body[2].Cmd.(*syntax.BashPPSend); send.Chan.Lit() != "ch" || send.Value.Lit() != "1" {
		t.Fatalf("send after JSON round trip = %#v", send)
	}
	decl := body[3].Cmd.(*syntax.BashPPShortDecl)
	if len(decl.Lhs) != 2 || decl.Recv == nil || decl.Recv.Chan.Lit() != "ch" {
		t.Fatalf("two-value receive after JSON round trip = %#v", decl)
	}
	if recv := body[4].Cmd.(*syntax.BashPPReceive); recv.Chan.Lit() != "ch" {
		t.Fatalf("standalone receive after JSON round trip = %#v", recv)
	}
	if cl := body[5].Cmd.(*syntax.BashPPClose); cl.Chan.Lit() != "ch" ||
		cl.Lparen.Col() != 7 || cl.Rparen.Col() != 10 || cl.End().Col() != 11 {
		t.Fatalf("close after JSON round trip = %#v", cl)
	}
	if rng := body[6].Cmd.(*syntax.BashPPRange); len(rng.Names) != 1 || rng.Chan.Lit() != "ch" {
		t.Fatalf("range after JSON round trip = %#v", rng)
	}
	sel := body[7].Cmd.(*syntax.BashPPSelect)
	if len(sel.Cases) != 3 {
		t.Fatalf("select arms after JSON round trip = %d, want 3", len(sel.Cases))
	}
	if _, ok := sel.Cases[0].Comm.(*syntax.BashPPShortDecl); !ok {
		t.Fatalf("select receive arm after JSON round trip = %T", sel.Cases[0].Comm)
	}
	if _, ok := sel.Cases[1].Comm.(*syntax.BashPPSend); !ok {
		t.Fatalf("select send arm after JSON round trip = %T", sel.Cases[1].Comm)
	}
	if !sel.Cases[2].Default || sel.Cases[2].Comm != nil {
		t.Fatalf("select default arm after JSON round trip = %#v", sel.Cases[2])
	}
}

func TestBashPPEmptyRangeRoundTrip(t *testing.T) {
	const src = "func f() {\n\tfor v := range ch { }\n}\n"
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "")
	if err != nil {
		t.Fatal(err)
	}
	var data bytes.Buffer
	if err := Encode(&data, f); err != nil {
		t.Fatal(err)
	}
	n, err := Decode(bytes.NewReader(data.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	rng := n.(*syntax.File).Stmts[0].Cmd.(*syntax.BashPPFuncDecl).Body.Stmts[0].Cmd.(*syntax.BashPPRange)
	if rng.Body == nil || len(rng.Body.Stmts) != 0 {
		t.Fatalf("decoded range body = %#v, want empty block", rng.Body)
	}
	var out strings.Builder
	if err := syntax.NewPrinter().Print(&out, n); err != nil {
		t.Fatal(err)
	}
	if out.String() != src {
		t.Fatalf("round trip = %q, want %q", out.String(), src)
	}
}

func TestBashPPScalarRangeTypedJSONRoundTrip(t *testing.T) {
	const src = "func f() {\n\tfor i, r := range \"é\" { }\n\tfor i := range n + 1 { }\n}\n"
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "")
	if err != nil {
		t.Fatal(err)
	}
	var data bytes.Buffer
	if err := Encode(&data, f); err != nil {
		t.Fatal(err)
	}
	n, err := Decode(bytes.NewReader(data.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	body := n.(*syntax.File).Stmts[0].Cmd.(*syntax.BashPPFuncDecl).Body.Stmts
	if _, ok := body[0].Cmd.(*syntax.BashPPRange).Expr.(*syntax.BashPPBasicLit); !ok {
		t.Fatalf("decoded string operand = %T", body[0].Cmd.(*syntax.BashPPRange).Expr)
	}
	if _, ok := body[1].Cmd.(*syntax.BashPPRange).Expr.(*syntax.BashPPBinaryExpr); !ok {
		t.Fatalf("decoded expression operand = %T", body[1].Cmd.(*syntax.BashPPRange).Expr)
	}
	var out strings.Builder
	if err := syntax.NewPrinter().Print(&out, n); err != nil {
		t.Fatal(err)
	}
	if out.String() != src {
		t.Fatalf("round trip = %q, want %q", out.String(), src)
	}
}

// TestBashPPRedirectSpacingRoundTrip ensures the AST-carried distinction
// between a shell redirect and a channel receive survives serialization. This
// matters when callers print a decoded function body or statement in isolation.
func TestBashPPRedirectSpacingRoundTrip(t *testing.T) {
	const src = "func f() {\n\t< -file\n}\n"
	f, err := syntax.NewParser(
		syntax.Variant(syntax.LangBashPP), syntax.KeepComments(true),
	).Parse(strings.NewReader(src), "")
	if err != nil {
		t.Fatal(err)
	}
	var data bytes.Buffer
	if err := Encode(&data, f); err != nil {
		t.Fatal(err)
	}
	n, err := Decode(bytes.NewReader(data.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	stmt := n.(*syntax.File).Stmts[0].Cmd.(*syntax.BashPPFuncDecl).Body.Stmts[0]
	var out strings.Builder
	if err := syntax.NewPrinter().Print(&out, stmt); err != nil {
		t.Fatal(err)
	}
	if out.String() != "< -file" {
		t.Fatalf("decoded redirect statement = %q, want %q", out.String(), "< -file")
	}
}
