// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package typedjson_test

import (
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/bashsharp/bashsharp/engine/syntax/typedjson"
)

func TestBashPPIfByteExactRoundTrip(t *testing.T) {
	const src = "func f() {\n\tif n := 2; n > 2 {\n\t\techo high\n\t} else if n == 2 {\n\t\techo equal\n\t} else {\n\t\techo low\n\t}\n}\n"
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "if.bpp")
	if err != nil {
		t.Fatal(err)
	}
	var first strings.Builder
	if err := typedjson.Encode(&first, f); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first.String(), `"Type":"BashPPIf"`) ||
		!strings.Contains(first.String(), `"Type":"BashPPBinaryExpr"`) {
		t.Fatalf("typed JSON lacks if/expression tags: %s", first.String())
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
		t.Fatalf("decode/re-encode changed bytes:\nfirst:  %s\nsecond: %s", first.String(), second.String())
	}
	var printed strings.Builder
	if err := syntax.NewPrinter().Print(&printed, node); err != nil {
		t.Fatal(err)
	}
	if printed.String() != src {
		t.Fatalf("decoded print = %q, want %q", printed.String(), src)
	}
	reparsed, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(printed.String()), "if.bpp")
	if err != nil {
		t.Fatal(err)
	}
	var third strings.Builder
	if err := typedjson.Encode(&third, reparsed); err != nil {
		t.Fatal(err)
	}
	if third.String() != first.String() {
		t.Fatalf("parse/print/reparse changed typed JSON bytes")
	}
}
