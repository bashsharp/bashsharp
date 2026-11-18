package syntax

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestBashPPScalarCallOperands(t *testing.T) {
	const source = "func main() {\n value := true && yes(\"hé\", 1 + 2)\n}\n"
	for _, bytewise := range []bool{false, true} {
		var input io.Reader = strings.NewReader(source)
		if bytewise {
			input = guardByteReader{input}
		}
		file, err := NewParser(Variant(LangBashPP)).Parse(input, "scalar.bpp")
		if err != nil {
			t.Fatal(err)
		}
		decl := file.Stmts[0].Cmd.(*BashPPFuncDecl).Body.Stmts[0].Cmd.(*BashPPShortDecl)
		binary, ok := decl.Expr.(*BashPPBinaryExpr)
		if !ok || binary.Op.Value != "&&" {
			t.Fatalf("logical AST=%T", decl.Expr)
		}
		call, ok := binary.Y.(*BashPPCall)
		if !ok || len(call.ArgExprs) != 2 || len(call.Args) != 2 {
			t.Fatalf("call=%#v", binary.Y)
		}
		if call.Pos().Line() != 2 || call.Pos().Col() != 19 || call.ArgExprs[0].Pos().Col() != 23 {
			t.Fatalf("positions %v/%v", call.Pos(), call.ArgExprs[0].Pos())
		}
		words, calls, adds := 0, 0, 0
		Walk(call, func(node Node) bool {
			switch n := node.(type) {
			case *Word:
				words++
			case *BashPPCall:
				calls++
			case *BashPPBinaryExpr:
				if n.Op.Value == "+" {
					adds++
				}
			}
			return true
		})
		if words != 0 || calls != 1 || adds != 1 {
			t.Fatalf("duplicate or missing typed ownership: words=%d calls=%d adds=%d", words, calls, adds)
		}
		var printed bytes.Buffer
		if err := NewPrinter().Print(&printed, file); err != nil {
			t.Fatal(err)
		}
		reparsed, err := NewParser(Variant(LangBashPP)).Parse(strings.NewReader(printed.String()), "scalar.bpp")
		if err != nil {
			t.Fatal(err)
		}
		var second bytes.Buffer
		if err := NewPrinter().Print(&second, reparsed); err != nil || second.String() != printed.String() {
			t.Fatalf("unstable print: %v", err)
		}
		// Typed children own mutations; preserved legacy words cannot silently
		// override a transformed expression during printing.
		call.ArgExprs[0].(*BashPPBasicLit).Value.Value = "\"changed\""
		printed.Reset()
		if err := NewPrinter().Print(&printed, file); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(printed.String(), "yes(\"changed\", 1 + 2)") || strings.Contains(printed.String(), "hé") {
			t.Fatal(printed.String())
		}
	}
}

func TestBashPPScalarCallShellIsolation(t *testing.T) {
	for _, lang := range []LangVariant{LangBashPP, LangBash, LangPOSIX} {
		for _, source := range []string{"x := false && printf yes\n", "f() { x := true || printf no; }\n"} {
			file, err := NewParser(Variant(lang)).Parse(guardByteReader{strings.NewReader(source)}, "")
			if err != nil {
				t.Fatal(err)
			}
			shell, typed := 0, 0
			Walk(file, func(node Node) bool {
				switch node.(type) {
				case *BinaryCmd:
					shell++
				case *BashPPBinaryExpr:
					typed++
				}
				return true
			})
			if shell != 1 || typed != 0 {
				t.Fatalf("dialect %v shell=%d typed=%d", lang, shell, typed)
			}
		}
	}
	file, err := NewParser(Variant(LangBashPP)).Parse(strings.NewReader("func main() {\n x := false && echo yes\n}\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := file.Stmts[0].Cmd.(*BashPPFuncDecl).Body.Stmts[0].Cmd.(*BinaryCmd); !ok {
		t.Fatal("ordinary command near miss was reinterpreted")
	}
}
