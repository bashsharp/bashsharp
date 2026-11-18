// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package syntax

import (
	"io"
	"strings"
	"testing"
	"testing/iotest"
)

func TestBashPPTypedConstDeclaration(t *testing.T) {
	src := "const Limit int8 = 7"
	for _, tc := range []struct {
		name string
		wrap func(io.Reader) io.Reader
	}{
		{"whole", func(r io.Reader) io.Reader { return r }},
		{"one byte", func(r io.Reader) io.Reader { return iotest.OneByteReader(r) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, err := NewParser(Variant(LangBashPP)).Parse(tc.wrap(strings.NewReader(src)), "decl.bpp")
			if err != nil {
				t.Fatal(err)
			}
			decl := bashppLastDecl(t, f)
			if decl.Site != StartConst || decl.Name.Value != "Limit" || decl.DeclType.Value != "int8" {
				t.Fatalf("declaration = %#v, want typed const Limit int8", decl)
			}
			if decl.InitExpr == nil || decl.InitExpr.Pos().Line() != 1 || decl.InitExpr.Pos().Col() != 20 {
				t.Fatalf("initializer position = %v, want line 1 column 20", decl.InitExpr.Pos())
			}
		})
	}
}

func TestBashPPTypedConstRequiresInitializer(t *testing.T) {
	bashppCheckDiagnostic(t, "const Limit int8", "invalid const statement; use command const to invoke a shell command")
}

func TestBashPPTypedConstClassicPOSIXIsolation(t *testing.T) {
	const src = "const Limit int8 = 7"
	for _, lang := range []LangVariant{LangBash, LangPOSIX} {
		for _, wrap := range []func(io.Reader) io.Reader{
			func(r io.Reader) io.Reader { return r },
			func(r io.Reader) io.Reader { return iotest.OneByteReader(r) },
		} {
			f, err := NewParser(Variant(lang)).Parse(wrap(strings.NewReader(src)), "")
			if err != nil {
				t.Fatalf("%v parse failed: %v", lang, err)
			}
			Walk(f, func(node Node) bool {
				if _, ok := node.(*BashPPDecl); ok {
					t.Fatalf("%v claimed a Bash++ typed const", lang)
				}
				return true
			})
		}
	}
}
