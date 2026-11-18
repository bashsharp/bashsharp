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

func TestBashPPTupleAssignRoundTrip(t *testing.T) {
	const src = "func pair() (int, int) {\n\treturn 1, 2\n}\nfunc main() {\n\tx, y = pair()\n}\n"
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "tuple.bpp")
	if err != nil {
		t.Fatal(err)
	}
	var encoded strings.Builder
	if err := typedjson.Encode(&encoded, file); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(encoded.String(), `"Names"`) || !strings.Contains(encoded.String(), `"Call"`) {
		t.Fatalf("typed JSON lost tuple assignment metadata: %s", encoded.String())
	}
	decoded, err := typedjson.Decode(strings.NewReader(encoded.String()))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, file) {
		t.Fatal("typed JSON changed tuple assignment tree")
	}
}
