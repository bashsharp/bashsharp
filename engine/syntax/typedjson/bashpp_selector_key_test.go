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

func TestBashPPStructSelectorKeyRoundTrip(t *testing.T) {
	const src = "type Leaf struct { Depth int }\ntype Gopher struct { Leaf }\nfunc main() {\n g := Gopher{Depth: 3}\n}\n"
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "selector-key.bpp")
	if err != nil {
		t.Fatal(err)
	}
	var encoded strings.Builder
	if err := typedjson.Encode(&encoded, file); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(encoded.String(), `"Key":{"Type":"BashPPIdent"`) {
		t.Fatalf("encoded selector key missing typed identity: %s", encoded.String())
	}
	decoded, err := typedjson.Decode(strings.NewReader(encoded.String()))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, file) {
		t.Fatal("typed JSON changed selector-key tree")
	}
}
