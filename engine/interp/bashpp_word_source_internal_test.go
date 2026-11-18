// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

// Sprint: #270; Story: #757; Story-ID: 609c89bfa598

import (
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// bashPPWordSource's literal fast path and pooled printer must spell every
// word exactly as a fresh printer does.
func TestBashPPWordSourceMatchesPrinter(t *testing.T) {
	words := []*syntax.Word{
		{Parts: []syntax.WordPart{&syntax.Lit{Value: "abc"}}},
		{Parts: []syntax.WordPart{&syntax.Lit{Value: "a\tb"}}},
		{Parts: []syntax.WordPart{&syntax.Lit{Value: ""}}},
		{Parts: []syntax.WordPart{&syntax.Lit{Value: "x[1]"}}},
		{Parts: []syntax.WordPart{&syntax.Lit{Value: "a"}, &syntax.Lit{Value: "b"}}},
	}
	f, err := syntax.NewParser().Parse(strings.NewReader("echo ${x} \"$y z\" 'q\tr' a$b\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	words = append(words, f.Stmts[0].Cmd.(*syntax.CallExpr).Args...)
	for i := 0; i < 2; i++ { // the second pass reuses pooled printers
		for _, w := range words {
			var want strings.Builder
			if err := syntax.NewPrinter().Print(&want, w); err != nil {
				t.Fatal(err)
			}
			if got := bashPPWordSource(w); got != want.String() {
				t.Errorf("bashPPWordSource = %q, want %q", got, want.String())
			}
		}
	}
}
