// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package typedjson_test

import (
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/bashsharp/bashsharp/engine/syntax/typedjson"
)

func TestBashPPFuncRoundTrip(t *testing.T) {
	const src = "func f(a, b int) (x, y int) {\n\tdefer g(a)\n\tx = a\n\ty = b\n\treturn\n}\n"
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "")
	if err != nil {
		t.Fatal(err)
	}
	var enc strings.Builder
	if err := typedjson.Encode(&enc, f); err != nil {
		t.Fatal(err)
	}
	node, err := typedjson.Decode(strings.NewReader(enc.String()))
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := syntax.NewPrinter().Print(&out, node); err != nil {
		t.Fatal(err)
	}
	if out.String() != src {
		t.Fatalf("round trip = %q, want %q", out.String(), src)
	}
}

func TestBashPPGenericFuncRoundTrip(t *testing.T) {
	const src = "func id[T any](v T) T {\n\treturn v\n}\nid[[]int](xs)\n"
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "")
	if err != nil {
		t.Fatal(err)
	}
	var enc strings.Builder
	if err := typedjson.Encode(&enc, f); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"TypeParams", "BashPPTypeParamType", "TypeArgs", "ArgType"} {
		if !strings.Contains(enc.String(), want) {
			t.Fatalf("encoded JSON missing %s: %s", want, enc.String())
		}
	}
	node, err := typedjson.Decode(strings.NewReader(enc.String()))
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := syntax.NewPrinter().Print(&out, node); err != nil {
		t.Fatal(err)
	}
	if out.String() != src {
		t.Fatalf("round trip = %q, want %q", out.String(), src)
	}
}

func TestBashPPGenericNamedTypeRoundTrip(t *testing.T) {
	const src = "type Box[T ~int\\|string] struct { Value T }\nvar b Box[int] = Box[int]{Value:7}\n"
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "")
	if err != nil {
		t.Fatal(err)
	}
	var enc strings.Builder
	if err := typedjson.Encode(&enc, f); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"TypeParams", "BashPPUnionType", "BashPPApproxType", "TypeArgs"} {
		if !strings.Contains(enc.String(), want) {
			t.Fatalf("encoded JSON missing %s: %s", want, enc.String())
		}
	}
	node, err := typedjson.Decode(strings.NewReader(enc.String()))
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	if err := syntax.NewPrinter().Print(&out, node); err != nil {
		t.Fatal(err)
	}
	reparsed, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(out.String()), "")
	if err != nil {
		t.Fatal(err)
	}
	var second strings.Builder
	if err := typedjson.Encode(&second, reparsed); err != nil {
		t.Fatal(err)
	}
	if second.String() != enc.String() {
		t.Fatal("generic named type JSON changed after print/reparse")
	}
}
