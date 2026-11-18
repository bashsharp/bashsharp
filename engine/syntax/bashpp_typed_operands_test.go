// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.
package syntax

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestBashPPTypedOperands(t *testing.T) {
	cases := []struct {
		name, src string
		check     func(*testing.T, *File)
	}{
		{"guard", "func positive(p *int) bool { return p != nil && *p > 0 }\n", func(t *testing.T, f *File) {
			st := f.Stmts[0].Cmd.(*BashPPFuncDecl).Body.Stmts[0]
			r, ok := st.Cmd.(*BashPPReturn)
			if !ok {
				t.Fatalf("return became %T", st.Cmd)
			}
			x, ok := r.Expr.(*BashPPBinaryExpr)
			if !ok || x.Op.Value != "&&" || len(st.Redirs) != 0 {
				t.Fatalf("guard: %#v", r)
			}
			if r.Results[0].Lit() != "p != nil && *p > 0" {
				t.Fatalf("legacy result: %q", r.Results[0].Lit())
			}
			if x.Pos().Col() != 37 || x.End().Col() != 55 {
				t.Fatalf("guard positions %v..%v", x.Pos(), x.End())
			}
			n := 0
			Walk(r, func(v Node) bool {
				if _, ok := v.(*BashPPBinaryExpr); ok {
					n++
				}
				return true
			})
			if n != 3 {
				t.Fatalf("walk: %d binary nodes", n)
			}
		}},
		{"parenthesis", "func main() {\n n := (1 + 2) * 3\n}\n", func(t *testing.T, f *File) {
			d := f.Stmts[0].Cmd.(*BashPPFuncDecl).Body.Stmts[0].Cmd.(*BashPPShortDecl)
			x := d.Expr.(*BashPPBinaryExpr).X.(*BashPPParenExpr)
			if x.Pos().Line() != 2 || x.Pos().Col() != 7 || x.End().Col() != 14 {
				t.Fatalf("paren positions %v..%v", x.Pos(), x.End())
			}
		}},
		{"raw", "func main() {\n s := `héllo $HOME \\n \\\nworld`\n}\n", func(t *testing.T, f *File) {
			d := f.Stmts[0].Cmd.(*BashPPFuncDecl).Body.Stmts[0].Cmd.(*BashPPShortDecl)
			x := d.Expr.(*BashPPBasicLit)
			if x.Value.Value != "`héllo $HOME \\n \\\nworld`" || x.Kind != "STRING" {
				t.Fatalf("literal: %#v", x)
			}
			if x.Pos().Line() != 2 || x.Pos().Col() != 7 || x.End().Line() != 3 || x.End().Col() != 7 {
				t.Fatalf("raw positions %v..%v", x.Pos(), x.End())
			}
			Walk(d, func(v Node) bool {
				if _, ok := v.(*CmdSubst); ok {
					t.Fatal("raw literal became command substitution")
				}
				return true
			})
		}},
		{"forwarded_inline", "func pair() (int, string) { return 7, \"seven\" }\nfunc forwarded() (int, string) { return pair() }\n", func(t *testing.T, f *File) {
			r := f.Stmts[1].Cmd.(*BashPPFuncDecl).Body.Stmts[0].Cmd.(*BashPPReturn)
			if r.Call == nil || r.Call.Fun[0].Value != "pair" || r.Results[0].Lit() != "pair()" {
				t.Fatalf("returned call: %#v", r)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for _, bytewise := range []bool{false, true} {
				var rd io.Reader = strings.NewReader(c.src)
				if bytewise {
					rd = guardByteReader{rd}
				}
				f, err := NewParser(Variant(LangBashPP)).Parse(rd, "operands.bpp")
				if err != nil {
					t.Fatal(err)
				}
				c.check(t, f)
				var first, second bytes.Buffer
				if err := NewPrinter().Print(&first, f); err != nil {
					t.Fatal(err)
				}
				f2, err := NewParser(Variant(LangBashPP)).Parse(guardByteReader{strings.NewReader(first.String())}, "operands.bpp")
				if err != nil {
					t.Fatal(err)
				}
				if err := NewPrinter().Print(&second, f2); err != nil {
					t.Fatal(err)
				}
				if first.String() != second.String() {
					t.Fatalf("print unstable: %q -> %q", first.String(), second.String())
				}
			}
		})
	}
}

func TestBashPPTypedOperandsShellIsolation(t *testing.T) {
	for _, lang := range []LangVariant{LangBashPP, LangBash, LangPOSIX} {
		for _, src := range []string{"s=`printf hello`\n", "echo `printf hello`\n", "x := `printf hello`\n", "f() { return 0 && echo yes; }\n", "(echo hello)\n"} {

			f, err := NewParser(Variant(lang)).Parse(guardByteReader{strings.NewReader(src)}, "")
			if err != nil {
				t.Fatal(err)
			}
			if lang == LangBashPP && src == "x := `printf hello`\n" {
				d, ok := f.Stmts[0].Cmd.(*BashPPShortDecl)
				if !ok {
					t.Fatalf("Go binding: %T", f.Stmts[0].Cmd)
				}
				if _, ok := d.Expr.(*BashPPBasicLit); !ok {
					t.Fatalf("raw literal: %#v", d)
				}
				continue
			}

			Walk(f, func(n Node) bool {
				switch n.(type) {
				case *BashPPShortDecl, *BashPPReturn, *BashPPBasicLit:
					t.Fatalf("%v claimed shell source %q: %T", lang, src, n)
				}
				return true
			})
			if strings.Contains(src, "`") {
				seen := false
				Walk(f, func(n Node) bool {
					if _, ok := n.(*CmdSubst); ok {
						seen = true
					}
					return true
				})
				if !seen {
					t.Fatal("lost shell backquote")
				}
			}
		}
	}
	for _, lang := range []LangVariant{LangBash, LangPOSIX} {
		if _, err := NewParser(Variant(lang)).Parse(guardByteReader{strings.NewReader("func f() { x := (1 + 2) }\n")}, ""); err == nil {
			t.Fatal("classic dialect accepted typed prefix")
		}
	}
	// Ordinary shell arguments inside a typed function still own backquotes.
	f, err := NewParser(Variant(LangBashPP)).Parse(guardByteReader{strings.NewReader("func f() {\n echo `printf hello`\n}\n")}, "")
	if err != nil {
		t.Fatal(err)
	}
	seen := false
	Walk(f, func(n Node) bool {
		if _, ok := n.(*CmdSubst); ok {
			seen = true
		}
		return true
	})
	if !seen {
		t.Fatal("typed body claimed shell argument")
	}
}

func TestBashPPTypedRawUnterminated(t *testing.T) {
	_, err := NewParser(Variant(LangBashPP)).Parse(guardByteReader{strings.NewReader("func f() {\n s := `missing\n")}, "raw.bpp")
	e, ok := err.(ParseError)
	if !ok || e.Pos.Line() != 2 || e.Pos.Col() != 7 || !strings.Contains(e.Text, "unterminated Bash++ raw string") {
		t.Fatalf("positioned raw error: %v", err)
	}
}
