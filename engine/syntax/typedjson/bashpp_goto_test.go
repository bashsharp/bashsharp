// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package typedjson_test

import (
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/bashsharp/bashsharp/engine/syntax/typedjson"
)

// The Bash++ parser has no label syntax; the labeled and goto nodes only
// arrive from the Go-source converter, so the tree is built by hand.
func TestBashPPGotoRoundTrip(t *testing.T) {
	lit := func(v string) *syntax.Lit { return &syntax.Lit{Value: v} }
	f := &syntax.File{Stmts: []*syntax.Stmt{
		{Cmd: &syntax.BashPPLabeled{Label: lit("again"), Stmt: &syntax.Stmt{Cmd: &syntax.BashPPBranch{Kw: lit("break"), Depth: 2}}}},
		{Cmd: &syntax.BashPPGoto{Kw: lit("goto"), Label: lit("again")}},
		{Cmd: &syntax.BashPPLabeled{Label: lit("end")}},
	}}
	var first strings.Builder
	if err := typedjson.Encode(&first, f); err != nil {
		t.Fatal(err)
	}
	for _, tag := range []string{`"Type":"BashPPLabeled"`, `"Type":"BashPPGoto"`} {
		if !strings.Contains(first.String(), tag) {
			t.Fatalf("typed JSON lacks %s: %s", tag, first.String())
		}
	}
	node, err := typedjson.Decode(strings.NewReader(first.String()))
	if err != nil {
		t.Fatal(err)
	}
	var second strings.Builder
	if err := typedjson.Encode(&second, node); err != nil {
		t.Fatal(err)
	}
	if second.String() != first.String() {
		t.Fatal("decode/re-encode changed bytes")
	}
	var printed strings.Builder
	if err := syntax.NewPrinter().Print(&printed, node); err != nil {
		t.Fatal(err)
	}
	const want = "again: break\ngoto again\nend:\n"
	if printed.String() != want {
		t.Fatalf("decoded print = %q, want %q", printed.String(), want)
	}
}
