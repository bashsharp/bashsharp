// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package typedjson_test

import (
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/bashsharp/bashsharp/engine/syntax/typedjson"
)

func TestBashPPKwargsDefaultsRoundTrip(t *testing.T) {
	const src = "func greet(name string, retries int = 3) {\n\techo $name\n}\ngreet(\"Ada\", retries: 7)\n"
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "")
	if err != nil {
		t.Fatal(err)
	}
	var encoded strings.Builder
	if err := typedjson.Encode(&encoded, f); err != nil {
		t.Fatal(err)
	}
	node, err := typedjson.Decode(strings.NewReader(encoded.String()))
	if err != nil {
		t.Fatal(err)
	}
	var printed strings.Builder
	if err := syntax.NewPrinter().Print(&printed, node); err != nil {
		t.Fatal(err)
	}
	if printed.String() != src {
		t.Fatalf("round trip = %q, want %q", printed.String(), src)
	}
}
