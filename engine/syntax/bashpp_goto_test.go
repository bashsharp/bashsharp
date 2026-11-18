// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package syntax

import (
	"bytes"
	"testing"
)

// bashppGotoFixture builds the tree the Go-source converter produces for
//
//	again: i++
//	goto again
//	end:
//
// by hand: the Bash++ parser has no label syntax, so these nodes only ever
// arrive from gosource.
func bashppGotoFixture() *File {
	lit := func(v string) *Lit { return &Lit{Value: v} }
	incr := &Stmt{Cmd: &BashPPIncDec{TargetWord: &Word{Parts: []WordPart{lit("i")}}, Target: &BashPPIdent{Name: lit("i")}, Op: lit("++"), Name: lit("i")}}
	return &File{Stmts: []*Stmt{
		{Cmd: &BashPPLabeled{Label: lit("again"), Stmt: incr}},
		{Cmd: &BashPPGoto{Kw: lit("goto"), Label: lit("again")}},
		{Cmd: &BashPPLabeled{Label: lit("end")}},
	}}
}

func TestBashPPGotoWalkAndPrint(t *testing.T) {
	f := bashppGotoFixture()
	var seen []string
	Walk(f, func(n Node) bool {
		switch n := n.(type) {
		case *BashPPLabeled:
			seen = append(seen, "label:"+n.Label.Value)
		case *BashPPGoto:
			seen = append(seen, "goto:"+n.Label.Value)
		case *BashPPIncDec:
			seen = append(seen, "incdec")
		}
		return true
	})
	want := []string{"label:again", "incdec", "goto:again", "label:end"}
	if len(seen) != len(want) {
		t.Fatalf("walk = %v, want %v", seen, want)
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("walk = %v, want %v", seen, want)
		}
	}
	var printed bytes.Buffer
	if err := NewPrinter().Print(&printed, f); err != nil {
		t.Fatal(err)
	}
	const wantText = "again: i++\ngoto again\nend:\n"
	if printed.String() != wantText {
		t.Fatalf("print = %q, want %q", printed.String(), wantText)
	}
}
