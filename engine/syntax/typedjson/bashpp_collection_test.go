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

func TestBashPPCollectionRoundTrip(t *testing.T) {
	const src = "func main() {\n\tx := map[string][]int{\"a\": {1, 2}}\n\ty := x[\"a\"][0]\n\tz := x[\"a\"][0:2]\n\tx[\"a\"][1] = 3\n}\n"
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "")
	if err != nil {
		t.Fatal(err)
	}
	var encoded strings.Builder
	if err := typedjson.Encode(&encoded, f); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"BashPPCompositeLit", "BashPPCollectionType", "BashPPIndexExpr", "BashPPSliceExpr"} {
		if !strings.Contains(encoded.String(), `"Type":"`+name+`"`) {
			t.Fatalf("JSON missed %s: %s", name, encoded.String())
		}
	}
	node, err := typedjson.Decode(strings.NewReader(encoded.String()))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(node, f) {
		t.Fatal("typed JSON changed collection tree")
	}
}

func TestBashPPCompositeTypeDeclRoundTrip(t *testing.T) {
	const src = "type A [2+1]int\ntype S = []string\ntype M map[string][]int\ntype P *A\n"
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "")
	if err != nil {
		t.Fatal(err)
	}
	var encoded strings.Builder
	if err := typedjson.Encode(&encoded, f); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"BashPPDecl", "BashPPCollectionType", "BashPPPointerType"} {
		if !strings.Contains(encoded.String(), `"Type":"`+name+`"`) {
			t.Fatalf("JSON missed %s: %s", name, encoded.String())
		}
	}
	node, err := typedjson.Decode(strings.NewReader(encoded.String()))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(node, f) {
		t.Fatal("typed JSON changed composite type declaration tree")
	}
}
