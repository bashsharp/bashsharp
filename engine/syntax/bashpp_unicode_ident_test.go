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

const bashPPUnicodeIdentSource = `type 数据 struct { 值 int }
func 计算(参数 int) {
	结果２ := 参数 + 1
	println(结果２)
}
`

func TestBashPPUnicodeIdentifierRules(t *testing.T) {
	for _, ident := range []string{"π", "变量２", "_δ9", "日本語"} {
		if !BashPPValidIdent(ident) {
			t.Errorf("BashPPValidIdent(%q) = false", ident)
		}
	}
	for _, ident := range []string{"２变量", "a\u0301", "Ⅻ", "😀x", "x-y", "for", string([]byte{0xff})} {
		if BashPPValidIdent(ident) {
			t.Errorf("BashPPValidIdent(%q) = true", ident)
		}
	}
	if ValidName("π") {
		t.Fatal("POSIX ValidName accepted a Unicode letter")
	}
}

func TestBashPPUnicodeIdentifierStreamingPrintWalk(t *testing.T) {
	parse := func(rd io.Reader) *File {
		file, err := NewParser(Variant(LangBashPP)).Parse(rd, "unicode.bpp")
		if err != nil {
			t.Fatal(err)
		}
		return file
	}
	buffered := parse(strings.NewReader(bashPPUnicodeIdentSource))
	streamed := parse(iotest.OneByteReader(strings.NewReader(bashPPUnicodeIdentSource)))
	if !reflect.DeepEqual(buffered, streamed) {
		t.Fatal("buffered and one-byte Unicode identifier trees differ")
	}
	fn := buffered.Stmts[1].Cmd.(*BashPPFuncDecl)
	nameAt := strings.Index(bashPPUnicodeIdentSource, "计算")
	if fn.Name.Pos().Offset() != uint(nameAt) || fn.Name.End().Offset() != uint(nameAt+len("计算")) {
		t.Fatalf("function-name byte positions = %d..%d, want %d..%d", fn.Name.Pos().Offset(), fn.Name.End().Offset(), nameAt, nameAt+len("计算"))
	}
	seen := map[string]bool{}
	Walk(buffered, func(node Node) bool {
		if lit, ok := node.(*Lit); ok && BashPPValidIdent(lit.Value) {
			seen[lit.Value] = true
		}
		return true
	})
	for _, want := range []string{"数据", "值", "计算", "参数", "结果２"} {
		if !seen[want] {
			t.Errorf("Walk missed identifier %q", want)
		}
	}
	var printed strings.Builder
	if err := NewPrinter().Print(&printed, buffered); err != nil {
		t.Fatal(err)
	}
	if printed.String() != bashPPUnicodeIdentSource {
		t.Fatalf("print = %q", printed.String())
	}
}

func TestBashPPInvalidUnicodeIdentifiersAreNotClaimed(t *testing.T) {
	const src = "func f() {\n\t２bad := 1\n\ta\u0301 := 2\n}\n"
	file, err := NewParser(Variant(LangBashPP)).Parse(strings.NewReader(src), "invalid-unicode.bpp")
	if err != nil {
		t.Fatal(err)
	}
	body := file.Stmts[0].Cmd.(*BashPPFuncDecl).Body
	for i, stmt := range body.Stmts {
		if _, ok := stmt.Cmd.(*CallExpr); !ok {
			t.Fatalf("invalid Unicode statement %d parsed as %T", i, stmt.Cmd)
		}
	}
	bashppCheckDiagnostic(t, "var 😀 = 3\n", "invalid var statement; use command var to invoke a shell command")
}

func TestBashPPUnicodeIdentifierDialectIsolation(t *testing.T) {
	for _, lang := range []LangVariant{LangBash, LangPOSIX} {
		for _, wrap := range []func(io.Reader) io.Reader{
			func(r io.Reader) io.Reader { return r },
			func(r io.Reader) io.Reader { return iotest.OneByteReader(r) },
		} {
			file, err := NewParser(Variant(lang)).Parse(wrap(strings.NewReader(bashPPUnicodeIdentSource)), "unicode.sh")
			if err == nil {
				Walk(file, func(node Node) bool {
					switch node.(type) {
					case *BashPPDecl, *BashPPFuncDecl, *BashPPShortDecl:
						t.Fatalf("%v produced a Bash++ Unicode declaration", lang)
					}
					return true
				})
			}
		}
	}
	top, err := NewParser(Variant(LangBashPP)).Parse(strings.NewReader("π ordinary-shell-argument\n"), "top.bpp")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := top.Stmts[0].Cmd.(*CallExpr); !ok {
		t.Fatalf("ordinary Unicode shell command parsed as %T", top.Stmts[0].Cmd)
	}
}
