// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package typedjson_test

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/bashsharp/bashsharp/engine/syntax/typedjson"
)

func TestBashPPPointerRoundTrip(t *testing.T) {
	src := "func f() {\n var p *int\n x := 1\n p2 := &x\n y := *p2\n q := new([]int)\n}\n"
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "")
	if err != nil {
		t.Fatal(err)
	}
	var first, second bytes.Buffer
	if err := typedjson.Encode(&first, f); err != nil {
		t.Fatal(err)
	}
	n, err := typedjson.Decode(strings.NewReader(first.String()))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f, n) {
		t.Fatal("typed JSON changed pointer tree")
	}
	if err := typedjson.Encode(&second, n); err != nil {
		t.Fatal(err)
	}
	if first.String() != second.String() {
		t.Fatal("pointer typed JSON bytes changed")
	}
}
