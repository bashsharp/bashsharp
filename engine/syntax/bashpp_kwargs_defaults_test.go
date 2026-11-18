// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package syntax

import (
	"io"
	"strings"
	"testing"
)

func TestBashPPKwargsDefaultsAST(t *testing.T) {
	const src = "func greet(name string, retries int = 3) {\n\techo $name $retries\n}\ngreet(\"Ada\", retries: 7)\n"
	for _, rd := range []io.Reader{strings.NewReader(src), funcOneByteReader{strings.NewReader(src)}} {
		f, err := NewParser(Variant(LangBashPP)).Parse(rd, "fixture.bpp")
		if err != nil {
			t.Fatal(err)
		}
		decl := f.Stmts[0].Cmd.(*BashPPFuncDecl)
		if len(decl.Params) != 2 || decl.Params[1].Default == nil || !decl.Params[1].Equals.IsValid() || !decl.Params[1].Default.Pos().After(decl.Params[1].Equals) {
			t.Fatalf("default AST = %#v", decl.Params)
		}
		call := f.Stmts[1].Cmd.(*BashPPCall)
		if len(call.Args) != 2 || len(call.ArgNames) != 1 || call.ArgNames[0].Value != "retries" {
			t.Fatalf("call AST = %#v", call)
		}
		if call.ArgNames[0].End().Offset()-call.ArgNames[0].Pos().Offset() != uint(len("retries")) {
			t.Fatalf("named argument position = %v..%v", call.ArgNames[0].Pos(), call.ArgNames[0].End())
		}
		var out strings.Builder
		if err := NewPrinter().Print(&out, f); err != nil {
			t.Fatal(err)
		}
		if out.String() != src {
			t.Fatalf("printed %q, want %q", out.String(), src)
		}
		seenName, seenDefault := false, false
		Walk(f, func(node Node) bool {
			switch n := node.(type) {
			case *Lit:
				seenName = seenName || n == call.ArgNames[0]
			case *Word:
				seenDefault = seenDefault || n == decl.Params[1].Default
			}
			return true
		})
		if !seenName || !seenDefault {
			t.Fatalf("Walk missed name=%v default=%v", seenName, seenDefault)
		}
	}
}

func TestBashPPKwargsDefaultsIsolation(t *testing.T) {
	for _, src := range []string{
		"greet name: Ada\n",
		"func greet(name string, retries int == 3) string {\n return name\n}\n",
	} {
		if diff := bashppParseDiff(t, src); diff != "" {
			t.Fatalf("near miss %q changed: %s", src, diff)
		}
	}
	const accepted = "func f(v int = 1) {}\nf(v: 2)\n"
	for _, lang := range []LangVariant{LangBash, LangPOSIX} {
		if _, err := NewParser(Variant(lang)).Parse(strings.NewReader(accepted), "fixture.bpp"); err == nil {
			t.Fatalf("%v accepted Bash# syntax", lang)
		}
	}
}
