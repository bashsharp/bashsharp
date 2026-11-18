// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package syntax

import (
	"io"
	"strings"
	"testing"
)

const bashppEnumSyntaxFixture = `type Color enum { Red; Green }
func label(c Color) string {
    switch c {
    case Red:
        return "red"
    case Green:
        return "green"
    }
}
printf '%s\n' label(Green)
`

func TestBashPPEnumSyntaxSurfaces(t *testing.T) {
	for _, reader := range []io.Reader{strings.NewReader(bashppEnumSyntaxFixture), funcOneByteReader{strings.NewReader(bashppEnumSyntaxFixture)}} {
		f, err := NewParser(Variant(LangBashPP)).Parse(reader, "enum.bpp")
		if err != nil {
			t.Fatal(err)
		}
		decl, ok := f.Stmts[0].Cmd.(*BashPPDecl)
		if !ok || decl.DeclType.Value != "enum" || len(decl.EnumMembers) != 2 {
			t.Fatalf("enum declaration = %#v", f.Stmts[0].Cmd)
		}
		fn := f.Stmts[1].Cmd.(*BashPPFuncDecl)
		sw, ok := fn.Body.Stmts[0].Cmd.(*BashPPSwitch)
		first, firstOK := sw.Arms[0].Exprs[0].(*BashPPIdent)
		second, secondOK := sw.Arms[1].Exprs[0].(*BashPPIdent)
		if !ok || len(sw.Arms) != 2 || !firstOK || !secondOK || first.Name.Value != "Red" || second.Name.Value != "Green" {
			t.Fatalf("switch = %#v", fn.Body.Stmts[0].Cmd)
		}
		if decl.Pos().Offset() != 0 || decl.End().Offset() != 30 || sw.Pos().Offset() != 64 || sw.End().Offset() != 154 {
			t.Fatalf("positions decl=[%d,%d) switch=[%d,%d)", decl.Pos().Offset(), decl.End().Offset(), sw.Pos().Offset(), sw.End().Offset())
		}
		seenSwitch, seenArms := false, 0
		Walk(f, func(n Node) bool {
			switch n.(type) {
			case *BashPPSwitch:
				seenSwitch = true
			case *BashPPSwitchArm:
				seenArms++
			}
			return true
		})
		if !seenSwitch || seenArms != 2 {
			t.Fatalf("Walk saw switch=%v arms=%d", seenSwitch, seenArms)
		}
		var out strings.Builder
		if err := NewPrinter(Indent(4)).Print(&out, f); err != nil {
			t.Fatal(err)
		}
		if out.String() != bashppEnumSyntaxFixture {
			t.Fatalf("print:\n%s\nwant:\n%s", out.String(), bashppEnumSyntaxFixture)
		}
	}
}

func TestBashPPEnumDialectAndEscapes(t *testing.T) {
	for _, src := range []string{
		"type Color enu '{' Red ';' Green '}'\n",
		"command type Color enum '{' Red ';' Green '}'\n",
		"\"type\" Color enum '{' Red ';' Green '}'\n",
	} {
		bashppCheckIdentical(t, strings.TrimSuffix(src, "\n"))
	}
	for _, lang := range []LangVariant{LangBash, LangPOSIX} {
		f, err := NewParser(Variant(lang)).Parse(strings.NewReader("type Color enum { Red; Green }\n"), "")
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := f.Stmts[0].Cmd.(*BashPPDecl); ok {
			t.Fatalf("%v constructed a Bash# enum", lang)
		}
	}
}
