// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package syntax

import (
	"strings"
	"testing"
)

func TestBashPPGoCallAndClassicIsolation(t *testing.T) {
	const src = "go worker(1, 2)\n"
	f, err := bashppParse(LangBashPP, src)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := f.Stmts[0].Cmd.(*BashPPGo); !ok {
		t.Fatalf("got %T, want *BashPPGo", f.Stmts[0].Cmd)
	}
	got, err := bashppPrint(f)
	if err != nil {
		t.Fatal(err)
	}
	if got != src {
		t.Fatalf("print = %q, want %q", got, src)
	}

	// Parentheses, rather than the word go, are the Class-R disambiguator.
	// This remains the Go toolchain command in every dialect.
	for _, lang := range []LangVariant{LangBash, LangPOSIX, LangBashPP} {
		f, err := bashppParse(lang, "go build ./...\nfoo <- bar\n")
		if err != nil {
			t.Fatalf("%v: %v", lang, err)
		}
		for i, stmt := range f.Stmts {
			if lang == LangBashPP && i == 1 {
				if _, ok := stmt.Cmd.(*BashPPSend); !ok {
					t.Fatalf("channel send: %T", stmt.Cmd)
				}
				continue
			}
			if _, ok := stmt.Cmd.(*CallExpr); !ok {
				t.Fatalf("%v: got %T, want shell CallExpr", lang, stmt.Cmd)
			}
		}
	}
}

func TestBashPPGoClosureAndEmptySelectRoundTrip(t *testing.T) {
	for _, src := range []string{"go func(x int) {\n\techo $x\n}(1)\n", "func f() {\n\tselect {}\n}\n"} {
		f, err := bashppParse(LangBashPP, src)
		if err != nil {
			t.Fatalf("parse %q: %v", src, err)
		}
		var out strings.Builder
		if err := NewPrinter().Print(&out, f); err != nil {
			t.Fatal(err)
		}
		if _, err := bashppParse(LangBashPP, out.String()); err != nil {
			t.Fatalf("reparse %q: %v", out.String(), err)
		}
	}
}

func TestBashPPEmptyGoBlocks(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		src  string
		want string
		body func(*File) *Block
	}{
		{
			name: "function compact",
			src:  "func f() {}\n",
			want: "func f() { }\n",
			body: func(f *File) *Block { return f.Stmts[0].Cmd.(*BashPPFuncDecl).Body },
		},
		{
			name: "function spaced",
			src:  "func f() { }\n",
			want: "func f() { }\n",
			body: func(f *File) *Block { return f.Stmts[0].Cmd.(*BashPPFuncDecl).Body },
		},
		{
			name: "go closure",
			src:  "go func() {}()\n",
			want: "go func() { }()\n",
			body: func(f *File) *Block { return f.Stmts[0].Cmd.(*BashPPGo).Call.FuncLit.Body },
		},
		{
			name: "range",
			src:  "func f() {\n\tfor v := range ch {}\n}\n",
			want: "func f() {\n\tfor v := range ch { }\n}\n",
			body: func(f *File) *Block {
				outer := f.Stmts[0].Cmd.(*BashPPFuncDecl).Body
				return outer.Stmts[0].Cmd.(*BashPPRange).Body
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f, err := bashppParse(LangBashPP, tc.src)
			if err != nil {
				t.Fatal(err)
			}
			body := tc.body(f)
			if body == nil || len(body.Stmts) != 0 || !body.Lbrace.IsValid() || !body.Rbrace.IsValid() {
				t.Fatalf("empty body = %#v", body)
			}
			got, err := bashppPrint(f)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("print = %q, want %q", got, tc.want)
			}
			if _, err := bashppParse(LangBashPP, got); err != nil {
				t.Fatalf("reparse printed source: %v", err)
			}
		})
	}
}

func TestBashPPCompactGoClosureSemicolonInsertion(t *testing.T) {
	t.Parallel()

	const src = "go func() { echo hi }()\n"
	f, err := bashppParse(LangBashPP, src)
	if err != nil {
		t.Fatal(err)
	}
	goStmt, ok := f.Stmts[0].Cmd.(*BashPPGo)
	if !ok || goStmt.Call == nil || goStmt.Call.FuncLit == nil {
		t.Fatalf("command = %#v, want go closure", f.Stmts[0].Cmd)
	}
	body := goStmt.Call.FuncLit.Body
	if len(body.Stmts) != 1 || body.Stmts[0].Semicolon.IsValid() {
		t.Fatalf("body statements = %#v, want one source statement without shell semicolon", body.Stmts)
	}
	got, err := bashppPrint(f)
	if err != nil {
		t.Fatal(err)
	}
	const want = "go func() { echo hi; }()\n"
	if got != want {
		t.Fatalf("print = %q, want %q", got, want)
	}
	if _, err := bashppParse(LangBashPP, got); err != nil {
		t.Fatalf("reparse printed source: %v", err)
	}
}

func TestBashPPEmptyGoBlocksWalk(t *testing.T) {
	t.Parallel()

	const src = "func empty() {}\n" +
		"go func() {}()\n" +
		"func ranges() {\n\tfor v := range ch {}\n}\n"
	f, err := bashppParse(LangBashPP, src)
	if err != nil {
		t.Fatal(err)
	}
	var blocks, funcs, literals, goStmts, ranges int
	Walk(f, func(n Node) bool {
		switch n.(type) {
		case *Block:
			blocks++
		case *BashPPFuncDecl:
			funcs++
		case *BashPPFuncLit:
			literals++
		case *BashPPGo:
			goStmts++
		case *BashPPRange:
			ranges++
		}
		return true
	})
	if blocks != 4 || funcs != 2 || literals != 1 || goStmts != 1 || ranges != 1 {
		t.Fatalf("walk counts: blocks=%d funcs=%d literals=%d go=%d ranges=%d", blocks, funcs, literals, goStmts, ranges)
	}
}

func TestBashPPGoBlockSeparatorClassicIsolation(t *testing.T) {
	t.Parallel()

	const src = "go func() { echo hi }()\n"
	for _, lang := range []LangVariant{LangBash, LangPOSIX} {
		if _, err := bashppParse(lang, src); err == nil {
			t.Fatalf("%s unexpectedly accepted Bash++ semicolon insertion", lang)
		}
	}
}
