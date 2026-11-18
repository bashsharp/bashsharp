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

func TestBashPPEmbeddedStructTypedJSON(t *testing.T) {
	const src = "type Inner struct { N int }\ntype Outer struct { Inner; *Inner }\n"
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "embed.bpp")
	if err != nil {
		t.Fatal(err)
	}
	var encoded strings.Builder
	if err := typedjson.Encode(&encoded, file); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(encoded.String(), `"Embedded":true`) {
		t.Fatalf("encoded tree lacks embedded marker: %s", encoded.String())
	}
	decoded, err := typedjson.Decode(strings.NewReader(encoded.String()))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, file) {
		t.Fatal("typed JSON changed embedded struct tree")
	}
}

func TestBashPPEmbeddedInterfaceTypedJSON(t *testing.T) {
	const src = "type Reader interface { Read() }\ntype Outer struct { Reader }\n"
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "embed-interface.bpp")
	if err != nil {
		t.Fatal(err)
	}
	var encoded strings.Builder
	if err := typedjson.Encode(&encoded, file); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(encoded.String(), `"Embedded":true`) || !strings.Contains(encoded.String(), `"BashPPInterfaceType"`) {
		t.Fatalf("encoded tree lacks embedded interface identity: %s", encoded.String())
	}
	decoded, err := typedjson.Decode(strings.NewReader(encoded.String()))
	if err != nil {
		t.Fatal(err)
	}
	var reencoded strings.Builder
	if err := typedjson.Encode(&reencoded, decoded); err != nil {
		t.Fatal(err)
	}
	if reencoded.String() != encoded.String() {
		t.Fatal("typed JSON changed embedded interface tree")
	}
}
