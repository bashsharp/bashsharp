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

func TestBashPPStructRoundTrip(t *testing.T) {
	const src = "type C struct { Name string; Values [1]int }\nfunc f() {\n x := C{Name: \"x\"}\n y := struct{N int}{N: 1}\n x.Name = \"y\"\n}\n"
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "")
	if err != nil {
		t.Fatal(err)
	}
	var encoded strings.Builder
	if err := typedjson.Encode(&encoded, f); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"BashPPStructType", "BashPPSelectorExpr", "BashPPCompositeLit"} {
		if !strings.Contains(encoded.String(), `"Type":"`+name+`"`) {
			t.Fatalf("JSON missed %s", name)
		}
	}
	node, err := typedjson.Decode(strings.NewReader(encoded.String()))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(node, f) {
		t.Fatal("typed JSON changed struct tree")
	}
}
