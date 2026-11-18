// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package typedjson_test

import (
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/bashsharp/bashsharp/engine/syntax/typedjson"
)

func TestBashPPSwitchByteExactRoundTrip(t *testing.T) {
	const src = "func f(n int) {\n\tswitch x := n + 1; x {\n\tcase 1, 2:\n\t\techo yes\n\tdefault:\n\t\techo no\n\t}\n}\n"
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "switch.bpp")
	if err != nil {
		t.Fatal(err)
	}
	var first strings.Builder
	if err := typedjson.Encode(&first, f); err != nil {
		t.Fatal(err)
	}
	for _, tag := range []string{`"Type":"BashPPSwitch"`, `"Type":"BashPPBinaryExpr"`} {
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
	if printed.String() != src {
		t.Fatalf("decoded print = %q, want %q", printed.String(), src)
	}
	reparsed, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(printed.String()), "switch.bpp")
	if err != nil {
		t.Fatal(err)
	}
	var third strings.Builder
	if err := typedjson.Encode(&third, reparsed); err != nil {
		t.Fatal(err)
	}
	if third.String() != first.String() {
		t.Fatal("parse/print/reparse changed typed JSON bytes")
	}
}
