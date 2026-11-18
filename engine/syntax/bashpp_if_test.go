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

func TestBashPPIfParsesInsideTypedFunction(t *testing.T) {
	src := "func f() {\n\tif true {\n\t\techo yes\n\t}\n}\n"
	f, err := NewParser(Variant(LangBashPP)).Parse(strings.NewReader(src), "if.bpp")
	if err != nil {
		t.Fatal(err)
	}
	fn := f.Stmts[0].Cmd.(*BashPPFuncDecl)
	if _, ok := fn.Body.Stmts[0].Cmd.(*BashPPIf); !ok {
		t.Fatalf("if command = %T, want *BashPPIf", fn.Body.Stmts[0].Cmd)
	}
}

func TestBashPPIfTreePositionsWalkAndStreaming(t *testing.T) {
	const src = "func f() {\n\tif n := 2; n > 2 {\n\t\techo high\n\t} else if n == 2 {\n\t\techo equal\n\t} else {\n\t\techo low\n\t}\n}\n"
	parse := func(rd io.Reader) *File {
		f, err := NewParser(Variant(LangBashPP)).Parse(rd, "if.bpp")
		if err != nil {
			t.Fatal(err)
		}
		return f
	}
	buffered := parse(strings.NewReader(src))
	streamed := parse(iotest.OneByteReader(strings.NewReader(src)))
	if !reflect.DeepEqual(buffered, streamed) {
		t.Fatalf("buffered and one-byte trees differ:\n%#v\n%#v", buffered, streamed)
	}
	root := buffered.Stmts[0].Cmd.(*BashPPFuncDecl).Body.Stmts[0].Cmd.(*BashPPIf)
	child := root.Else.(*BashPPIf)
	els := child.Else.(*Block)
	if root.Site != StartGoIf || child.Site != StartGoIf || root.Init == nil || !root.Init.GoRegion {
		t.Fatalf("if provenance/init = %v/%v %#v", root.Site, child.Site, root.Init)
	}
	for label, got := range map[string]Pos{
		"if":        root.If,
		"init :=":   root.Init.OpPos,
		"init semi": root.Semicolon,
		"then {":    root.Then.Lbrace,
		"then }":    root.Then.Rbrace,
		"else":      root.ElsePos,
		"else if":   child.If,
		"last else": child.ElsePos,
		"else {":    els.Lbrace,
		"else }":    els.Rbrace,
	} {
		needle := map[string]string{
			"if": "if n", "init :=": ":=", "init semi": ";", "then {": "{\n\t\techo high",
			"then }": "} else if", "else": "else if", "else if": "if n ==", "last else": "else {",
			"else {": "{\n\t\techo low", "else }": "\t}\n}\n",
		}[label]
		off := strings.Index(src, needle)
		if label == "else }" {
			off++
		}
		want := bashppIfTestPos(src, off)
		if got != want {
			t.Errorf("%s position = %v, want %v", label, got, want)
		}
	}
	condStart := strings.Index(src, "n > 2")
	if root.Cond.Pos() != bashppIfTestPos(src, condStart) || root.Cond.End() != bashppIfTestPos(src, condStart+len("n > 2")) {
		t.Fatalf("condition Pos/End = %v/%v", root.Cond.Pos(), root.Cond.End())
	}
	initStart := strings.Index(src, "n := 2")
	if root.Init.Pos() != bashppIfTestPos(src, initStart) || root.Init.End() != bashppIfTestPos(src, initStart+len("n := 2")) {
		t.Fatalf("init Pos/End = %v/%v", root.Init.Pos(), root.Init.End())
	}
	if root.Pos() != root.If || root.End() != els.End() {
		t.Fatalf("if Pos/End = %v/%v, want %v/%v", root.Pos(), root.End(), root.If, els.End())
	}

	var seen []string
	Walk(root, func(n Node) bool {
		if n != nil {
			seen = append(seen, reflect.TypeOf(n).String())
		}
		return true
	})
	for _, typ := range []string{"*syntax.BashPPShortDecl", "*syntax.BashPPBinaryExpr", "*syntax.Block", "*syntax.BashPPIf"} {
		found := false
		for _, got := range seen {
			found = found || got == typ
		}
		if !found {
			t.Errorf("Walk did not visit %s: %v", typ, seen)
		}
	}

	var printed strings.Builder
	if err := NewPrinter().Print(&printed, buffered); err != nil {
		t.Fatal(err)
	}
	if printed.String() != src {
		t.Fatalf("print = %q, want %q", printed.String(), src)
	}
}

func bashppIfTestPos(src string, off int) Pos {
	prefix := src[:off]
	line := 1 + strings.Count(prefix, "\n")
	col := off - strings.LastIndex(prefix, "\n")
	return NewPos(uint(off), uint(line), uint(col))
}

func TestBashPPTopLevelBraceIfStaysShell(t *testing.T) {
	const src = "if true { echo yes; }\n"
	parse := func(lang LangVariant, rd io.Reader) (*File, error) {
		return NewParser(Variant(lang), RecoverErrors(4)).Parse(rd, "top.sh")
	}
	bashFile, bashErr := parse(LangBash, strings.NewReader(src))
	if bashErr == nil {
		t.Fatal("classic Bash unexpectedly accepted brace-form Go if")
	}
	for _, bytewise := range []bool{false, true} {
		var rd io.Reader = strings.NewReader(src)
		if bytewise {
			rd = iotest.OneByteReader(rd)
		}
		ppFile, ppErr := parse(LangBashPP, rd)
		if ppErr == nil || ppErr.Error() != bashErr.Error() {
			t.Fatalf("bytewise=%v diagnostics differ:\nbash: %v\npp:   %v", bytewise, bashErr, ppErr)
		}
		if !reflect.DeepEqual(ppFile, bashFile) {
			t.Fatalf("bytewise=%v partial trees differ after fallback", bytewise)
		}
	}
}

func TestBashPPIfFallbackPreservesShell(t *testing.T) {
	tests := []struct {
		name string
		lang LangVariant
		src  string
	}{
		{"top-level Bash++", LangBashPP, "if true; then echo yes; fi\n"},
		{"Bash", LangBash, "if true; then echo yes; fi\n"},
		{"POSIX", LangPOSIX, "if true; then echo yes; fi\n"},
		{"inside typed function", LangBashPP, "func f() {\n\tif { true; }; then echo yes; fi\n}\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f, err := NewParser(Variant(test.lang)).Parse(iotest.OneByteReader(strings.NewReader(test.src)), "")
			if err != nil {
				t.Fatal(err)
			}
			stmt := f.Stmts[0]
			if fn, ok := stmt.Cmd.(*BashPPFuncDecl); ok {
				stmt = fn.Body.Stmts[0]
			}
			if _, ok := stmt.Cmd.(*IfClause); !ok {
				t.Fatalf("command = %T, want classic *IfClause", stmt.Cmd)
			}
			var out strings.Builder
			if err := NewPrinter().Print(&out, f); err != nil {
				t.Fatal(err)
			}
			if out.String() != test.src {
				t.Fatalf("fallback print = %q, want %q", out.String(), test.src)
			}
		})
	}
}

func TestBashPPIfMalformedDiagnostics(t *testing.T) {
	for _, test := range []struct{ src, want string }{
		{"func f() { if 1 + { echo no } }", "bash++ if condition must be a scalar expression"},
		{"func f() { if n = 1; n > 0 { echo no } }", "bash++ if init must be a short declaration"},
	} {
		_, err := NewParser(Variant(LangBashPP)).Parse(strings.NewReader(test.src), "bad.bpp")
		if err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("Parse(%q) error = %v, want %q", test.src, err, test.want)
		}
	}
}
