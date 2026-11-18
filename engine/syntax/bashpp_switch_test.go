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

const bashppSwitchFixture = `func classify(n int) {
	switch x := n + 1; x {
	case 1, 2:
		echo low
	case 3:
		echo three
	default:
		echo other
	}
	switch {
	case n < 0:
		echo negative
	default:
		echo nonnegative
	}
}
`

func parseBashPPSwitch(t *testing.T, rd io.Reader) *File {
	t.Helper()
	f, err := NewParser(Variant(LangBashPP)).Parse(rd, "switch.bpp")
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestBashPPSwitchFormsPositionsWalkAndStreaming(t *testing.T) {
	buffered := parseBashPPSwitch(t, strings.NewReader(bashppSwitchFixture))
	streamed := parseBashPPSwitch(t, iotest.OneByteReader(strings.NewReader(bashppSwitchFixture)))
	if !reflect.DeepEqual(buffered, streamed) {
		t.Fatalf("buffered and one-byte trees differ:\n%#v\n%#v", buffered, streamed)
	}
	body := buffered.Stmts[0].Cmd.(*BashPPFuncDecl).Body
	tagged := body.Stmts[0].Cmd.(*BashPPSwitch)
	tagless := body.Stmts[1].Cmd.(*BashPPSwitch)
	if _, ok := tagged.Init.(*BashPPShortDecl); !ok || tagged.Tag == nil || !tagged.Semicolon.IsValid() {
		t.Fatalf("tagged switch header = %#v", tagged)
	}
	if tagless.Init != nil || tagless.Tag != nil || tagless.Semicolon.IsValid() {
		t.Fatalf("tagless switch header = %#v", tagless)
	}
	if len(tagged.Arms) != 3 || len(tagged.Arms[0].Exprs) != 2 || len(tagged.Arms[1].Exprs) != 1 || len(tagged.Arms[2].Exprs) != 0 {
		t.Fatalf("tagged switch arms = %#v", tagged.Arms)
	}
	for label, got := range map[string]Pos{
		"switch": tagged.Switch, "semicolon": tagged.Semicolon,
		"open": tagged.Lbrace, "first case": tagged.Arms[0].Case,
		"first comma": tagged.Arms[0].Commas[0].Pos(), "first colon": tagged.Arms[0].Colon,
		"close": tagged.Rbrace,
	} {
		needle := map[string]string{
			"switch": "switch x :=", "semicolon": "; x {", "open": "{\n\tcase 1",
			"first case": "case 1,", "first comma": ", 2:", "first colon": ":\n\t\techo low",
			"close": "\t}\n\tswitch {",
		}[label]
		off := strings.Index(bashppSwitchFixture, needle)
		if label == "semicolon" || label == "first comma" || label == "first colon" {
			off += strings.Index(needle, map[string]string{"semicolon": ";", "first comma": ",", "first colon": ":"}[label])
		} else if label == "close" {
			off++
		}
		if off < 0 || got.Offset() != uint(off) {
			t.Errorf("%s offset = %d, want %d", label, got.Offset(), off)
		}
	}
	var seen []string
	Walk(tagged, func(n Node) bool {
		if n != nil {
			seen = append(seen, reflect.TypeOf(n).String())
		}
		return true
	})
	for _, want := range []string{"*syntax.BashPPSwitch", "*syntax.BashPPShortDecl", "*syntax.BashPPBinaryExpr", "*syntax.BashPPSwitchArm"} {
		if !slicesContains(seen, want) {
			t.Errorf("Walk omitted %s: %v", want, seen)
		}
	}
	var printed bytes.Buffer
	if err := NewPrinter().Print(&printed, buffered); err != nil {
		t.Fatal(err)
	}
	if printed.String() != bashppSwitchFixture {
		t.Fatalf("print = %q, want %q", printed.String(), bashppSwitchFixture)
	}
	reparsed := parseBashPPSwitch(t, strings.NewReader(printed.String()))
	if !reflect.DeepEqual(buffered, reparsed) {
		t.Fatal("parse/print/reparse changed tree")
	}
}

func TestBashPPSwitchPreservesEnumAndDialectBoundaries(t *testing.T) {
	const top = "switch x { case 1: echo no; }\n"
	for _, lang := range []LangVariant{LangBashPP, LangBash, LangPOSIX} {
		f, _ := NewParser(Variant(lang), RecoverErrors(4)).Parse(strings.NewReader(top), "")
		if f != nil {
			Walk(f, func(n Node) bool {
				if _, ok := n.(*BashPPSwitch); ok {
					t.Fatalf("top-level %v produced BashPPSwitch", lang)
				}
				return true
			})
		}
	}
	f := parseBashPPSwitch(t, strings.NewReader(bashppEnumSyntaxFixture))
	sw := f.Stmts[1].Cmd.(*BashPPFuncDecl).Body.Stmts[0].Cmd.(*BashPPSwitch)
	if id, ok := sw.Tag.(*BashPPIdent); !ok || id.Name.Value != "c" || len(sw.Arms[0].Exprs) != 1 {
		t.Fatalf("enum switch was not represented as expression switch: %#v", sw)
	}
	const classic = "func f() {\n\tswitch plain words\n}\n"
	classicFile := parseBashPPSwitch(t, strings.NewReader(classic))
	cmd := classicFile.Stmts[0].Cmd.(*BashPPFuncDecl).Body.Stmts[0].Cmd
	if _, ok := cmd.(*CallExpr); !ok {
		t.Fatalf("switch-like shell command = %T, want *CallExpr", cmd)
	}
	var printed bytes.Buffer
	if err := NewPrinter().Print(&printed, classicFile); err != nil || printed.String() != classic {
		t.Fatalf("classic switch print/error = %q/%v", printed.String(), err)
	}
}

func TestBashPPSwitchStableDiagnostics(t *testing.T) {
	for _, test := range []struct{ src, want string }{
		{"func f() { switch echo hi; n { default: } }", "bash++ switch init must be a scalar short declaration, assignment, or inc-dec statement"},
		{"func f() { switch 1 + { default: } }", "bash++ switch tag must be a scalar expression"},
		{"func f() { switch n { case 1, nope +: echo no } }", "bash++ switch case must contain scalar expressions"},
		{"func f() { switch n { default: echo one; default: echo two } }", "bash++ switch has multiple default clauses"},
	} {
		parseErr := func(rd io.Reader) string {
			_, err := NewParser(Variant(LangBashPP)).Parse(rd, "bad.bpp")
			if err == nil {
				t.Fatalf("Parse(%q) succeeded", test.src)
			}
			return err.Error()
		}
		buffered := parseErr(strings.NewReader(test.src))
		streamed := parseErr(iotest.OneByteReader(strings.NewReader(test.src)))
		if buffered != streamed || !strings.Contains(buffered, test.want) {
			t.Errorf("Parse(%q): buffered=%q streamed=%q, want %q", test.src, buffered, streamed, test.want)
		}
	}
}

func TestBashPPSwitchAddsBranchControlSyntax(t *testing.T) {
	const src = "func f() { switch { case true: fallthrough; default: break } }\n"
	f := parseBashPPSwitch(t, strings.NewReader(src))
	arms := f.Stmts[0].Cmd.(*BashPPFuncDecl).Body.Stmts[0].Cmd.(*BashPPSwitch).Arms
	for i, arm := range arms {
		if _, ok := arm.Stmts[0].Cmd.(*BashPPBranch); !ok {
			t.Fatalf("arm %d statement = %T, want *BashPPBranch", i, arm.Stmts[0].Cmd)
		}
	}
}
