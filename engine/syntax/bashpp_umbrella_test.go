// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package syntax

import (
	"strings"
	"testing"
)

// The Sprint 113 umbrella closure (#126, Story-ID 34489edcbe2e) for the
// syntax layer: one script exercising every accepted Day-1 dispatch form —
// var, const, type, := and the Go-form call — proving in a single place that
// each parses to its typed node with the classification stored exactly once,
// and that the very same Class E source stays plain shell under LangBash and
// LangPOSIX. The per-form tests pin each site's edge cases; this one pins the
// umbrella's shape so a regression in any one arm is visible at the story
// level too.

// bashppUmbrellaNodes walks f and returns every Bash++ node in source order.
func bashppUmbrellaNodes(f *File) []Node {
	var nodes []Node
	Walk(f, func(n Node) bool {
		switch n.(type) {
		case *BashPPDecl, *BashPPShortDecl, *BashPPCall, *BashPPImport,
			*BashPPImportSpec, *BashPPFuncDecl, *BashPPField, *BashPPReturn,
			*BashPPDefer, *BashPPIf:
			nodes = append(nodes, n)
		}
		return true
	})
	return nodes
}

func TestBashPPUmbrellaClassificationOnce(t *testing.T) {
	t.Parallel()

	src := "var x = 1\nconst K = 2\ntype ID = string\nn := 39\nm := f(1)\nf(1, 2)\n"
	f, err := NewParser(Variant(LangBashPP)).Parse(strings.NewReader(src), "")
	if err != nil {
		t.Fatal(err)
	}

	nodes := bashppUmbrellaNodes(f)
	if len(nodes) != 7 {
		t.Fatalf("got %d Bash++ nodes, want 7 (three decls, two short decls, "+
			"the short decl's nested call, one call): %#v", len(nodes), nodes)
	}

	// The three keyword declarations: the site is the classification, and it
	// lives on the node — Kw records only the spelling.
	for i, want := range []StartSite{StartVar, StartConst, StartTypeDecl} {
		decl, ok := nodes[i].(*BashPPDecl)
		if !ok {
			t.Fatalf("node %d is %T, want *BashPPDecl", i, nodes[i])
		}
		if decl.Site != want {
			t.Fatalf("node %d Site = %v, want %v", i, decl.Site, want)
		}
	}
	if decl := nodes[2].(*BashPPDecl); !decl.Alias || decl.DeclType == nil || decl.DeclType.Value != "string" {
		t.Fatalf("type alias decl = %+v, want Alias=true DeclType=string", decl)
	}

	// The := split: the scalar form is Class E, the call form Class R, and the
	// class is recorded on the node rather than re-derived downstream.
	scalar, ok := nodes[3].(*BashPPShortDecl)
	if !ok || scalar.Class != ClassE || scalar.Call != nil {
		t.Fatalf("scalar := node = %#v, want *BashPPShortDecl Class=E Call=nil", nodes[3])
	}
	call, ok := nodes[4].(*BashPPShortDecl)
	if !ok || call.Class != ClassR || call.Call == nil {
		t.Fatalf("call := node = %#v, want *BashPPShortDecl Class=R with Call", nodes[4])
	}
	if _, ok := nodes[5].(*BashPPCall); !ok {
		t.Fatalf("node 5 is %T, want the short decl's nested *BashPPCall", nodes[5])
	}
	if _, ok := nodes[6].(*BashPPCall); !ok {
		t.Fatalf("node 6 is %T, want the command-position *BashPPCall", nodes[6])
	}

	// The printer round-trips the whole set from the typed nodes alone.
	var out strings.Builder
	if err := NewPrinter().Print(&out, f); err != nil {
		t.Fatal(err)
	}
	if out.String() != src {
		t.Fatalf("print round trip produced %q, want %q", out.String(), src)
	}
}

// TestBashPPUmbrellaClassicParseIsolation proves the Class E umbrella subset
// stays plain shell outside the dialect: same bytes, LangBash and LangPOSIX,
// zero Bash++ nodes and an identical print. The Class R forms need no row
// here because both classic grammars reject them outright.
func TestBashPPUmbrellaClassicParseIsolation(t *testing.T) {
	t.Parallel()

	src := "var x = 1\nconst K = 2\ntype ID = string\nn := 39\n"
	for _, lang := range []LangVariant{LangBash, LangPOSIX} {
		t.Run(lang.String(), func(t *testing.T) {
			t.Parallel()
			f, err := NewParser(Variant(lang)).Parse(strings.NewReader(src), "")
			if err != nil {
				t.Fatal(err)
			}
			if nodes := bashppUmbrellaNodes(f); len(nodes) != 0 {
				t.Fatalf("classic %v parse produced Bash++ nodes: %#v", lang, nodes)
			}
			for i, stmt := range f.Stmts {
				if _, ok := stmt.Cmd.(*CallExpr); !ok {
					t.Fatalf("stmt %d is %T, want the plain *CallExpr bash runs today", i, stmt.Cmd)
				}
			}
			var out strings.Builder
			if err := NewPrinter().Print(&out, f); err != nil {
				t.Fatal(err)
			}
			if out.String() != src {
				t.Fatalf("classic print produced %q, want the source unchanged %q", out.String(), src)
			}
		})
	}

	// The Class R members of the umbrella are rejected by the classic grammars,
	// which is what makes claiming them purely additive.
	for _, lang := range []LangVariant{LangBash, LangPOSIX} {
		for _, bad := range []string{"m := f(1)\n", "f(1, 2)\n"} {
			if _, err := NewParser(Variant(lang)).Parse(strings.NewReader(bad), ""); err == nil {
				t.Fatalf("classic %v accepted %q, want a syntax error", lang, bad)
			}
		}
	}
}
