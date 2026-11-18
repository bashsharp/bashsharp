// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package typedjson_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/bashsharp/bashsharp/engine/syntax/typedjson"
)

func TestBashPPInterfaceRoundTrip(t *testing.T) {
	const src = `type Reader interface { Read(string) int }
type Closer interface { Close() }
type Shower interface { Reader; Closer }
func f() {
	x, ok := i.(Count)
	switch v := i.(type) {
	case nil:
		echo nil
	case Count:
		echo "$v"
	default:
		echo other
	}
}
`
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "")
	if err != nil {
		t.Fatal(err)
	}
	var first bytes.Buffer
	if err := typedjson.Encode(&first, f); err != nil {
		t.Fatal(err)
	}
	for _, tag := range []string{"BashPPInterfaceType", `"Elems"`, `"Embedded"`, "BashPPTypeAssertExpr", `"Name":{"Pos"`} {
		if !strings.Contains(first.String(), tag) {
			t.Fatalf("typed JSON lacks %s: %s", tag, first.String())
		}
	}
	node, err := typedjson.Decode(strings.NewReader(first.String()))
	if err != nil {
		t.Fatal(err)
	}
	var second bytes.Buffer
	if err := typedjson.Encode(&second, node); err != nil {
		t.Fatal(err)
	}
	if first.String() != second.String() {
		t.Fatal("typed JSON changed interface tree")
	}
}
