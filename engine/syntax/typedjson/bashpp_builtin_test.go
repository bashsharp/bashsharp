// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package typedjson_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/bashsharp/bashsharp/engine/syntax/typedjson"
)

func TestBashPPBuiltinCallRoundTrip(t *testing.T) {
	const src = "func main() {\n\ts := make(map[string][]int, 2)\n\tn := len(s)\n}\n"
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "")
	if err != nil {
		t.Fatal(err)
	}
	var encoded strings.Builder
	if err := typedjson.Encode(&encoded, f); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(encoded.String(), `"ArgType"`) || !strings.Contains(encoded.String(), `"Type":"BashPPCollectionType"`) {
		t.Fatalf("typed JSON lost make type metadata: %s", encoded.String())
	}
	node, err := typedjson.Decode(strings.NewReader(encoded.String()))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(node, f) {
		t.Fatal("typed JSON changed builtin call tree")
	}
}
