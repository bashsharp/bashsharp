// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package typedjson_test

import (
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/bashsharp/bashsharp/engine/syntax/typedjson"
)

func TestBashPPBranchByteExactRoundTrip(t *testing.T) {
	const src = "func f() {\n\tfor {\n\t\tswitch {\n\t\tcase true:\n\t\t\tfallthrough\n\t\tdefault:\n\t\t\tbreak\n\t\t}\n\t\tcontinue\n\t}\n}\n"
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "branch.bpp")
	if err != nil {
		t.Fatal(err)
	}
	var first strings.Builder
	if err := typedjson.Encode(&first, f); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(first.String(), `"Type":"BashPPBranch"`); got != 3 {
		t.Fatalf("typed JSON branch tags = %d, want 3: %s", got, first.String())
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
}
