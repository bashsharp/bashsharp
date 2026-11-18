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

func TestBashPPUnicodeIdentifierRoundTrip(t *testing.T) {
	const src = "func 计算(参数 int) {\n\t结果２ := 参数 + 1\n}\n"
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "unicode.bpp")
	if err != nil {
		t.Fatal(err)
	}
	var encoded strings.Builder
	if err := typedjson.Encode(&encoded, file); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"计算", "参数", "结果２"} {
		if !strings.Contains(encoded.String(), want) {
			t.Fatalf("encoded tree lacks %q: %s", want, encoded.String())
		}
	}
	decoded, err := typedjson.Decode(strings.NewReader(encoded.String()))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, file) {
		t.Fatal("typed JSON changed Unicode identifier tree")
	}
}
