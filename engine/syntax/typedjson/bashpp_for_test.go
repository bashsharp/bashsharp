// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package typedjson_test

import (
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/bashsharp/bashsharp/engine/syntax/typedjson"
)

func TestBashPPForByteExactRoundTrip(t *testing.T) {
	const src = "func f() {\n\tfor i := 0; i < 3; i++ {\n\t\techo $i\n\t}\n}\n"
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "for.bpp")
	if err != nil {
		t.Fatal(err)
	}
	var first strings.Builder
	if err := typedjson.Encode(&first, f); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first.String(), `"Type":"BashPPFor"`) || !strings.Contains(first.String(), `"Type":"BashPPIncDec"`) {
		t.Fatalf("typed JSON lacks for/inc-dec tags: %s", first.String())
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
		t.Fatalf("decode/re-encode changed bytes")
	}
	var printed strings.Builder
	if err := syntax.NewPrinter().Print(&printed, node); err != nil {
		t.Fatal(err)
	}
	if printed.String() != src {
		t.Fatalf("decoded print = %q, want %q", printed.String(), src)
	}
}
