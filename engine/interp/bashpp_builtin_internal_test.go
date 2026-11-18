// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Sprint: #270; Story: #756; Story-ID: a0503101d15a

func TestSprint270BuiltinCollectionUsesUnderlyingShape(t *testing.T) {
	r := &Runner{bashPPTypes: map[string]bashPPType{
		"ImportedSlice": {typeExpr: &syntax.BashPPCollectionType{Kind: "slice", Element: &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "int"}}}},
	}}
	arg := bashPPBuiltinArg{
		value: []any{1, 2},
		meta:  &bashPPCollectionMeta{kind: "native", typ: &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "ImportedSlice"}}},
	}
	if _, ok := r.bashPPBuiltinCollection(arg, "slice"); !ok {
		t.Fatal("underlying slice shape rejected because metadata kind differed")
	}

	arg.meta.typ = &syntax.BashPPCollectionType{Kind: "map", Key: &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "string"}}, Element: &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "int"}}}
	if _, ok := r.bashPPBuiltinCollection(arg, "slice"); ok {
		t.Fatal("non-slice shape admitted as append destination")
	}
}

func TestSprint270BuiltinCollectionRejectsOpaqueNativeSlice(t *testing.T) {
	r := &Runner{}
	arg := bashPPBuiltinArg{
		value: &bashPPBridgeValue{Kind: "slice", Type: "[]int"},
		meta:  &bashPPCollectionMeta{kind: "native", typ: &syntax.BashPPCollectionType{Kind: "slice", Element: &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "int"}}}},
	}
	if _, ok := r.bashPPBuiltinCollection(arg, "slice"); ok {
		t.Fatal("opaque native carrier admitted without interpreted slice storage")
	}
}

func TestSprint270BuiltinArgMaterializesBridgeSlice(t *testing.T) {
	r := &Runner{}
	arg := bashPPBuiltinArg{value: &bashPPBridgeValue{
		Kind: "slice",
		Type: "[]string",
		Elements: []bashPPBridgeValue{
			{Kind: "string", Type: "string", Text: "A=B"},
		},
	}}
	if err := r.goSourceMaterializeBuiltinBridgeCollection(&arg); err != nil {
		t.Fatal(err)
	}
	shape, ok := r.bashPPBuiltinCollection(arg, "slice")
	if !ok {
		t.Fatal("materialized bridge slice rejected as append destination")
	}
	if got := bashPPTypeText(shape.Element); got != "string" {
		t.Fatalf("element type = %s, want string", got)
	}
	values, ok := arg.value.([]any)
	if !ok || len(values) != 1 || values[0] != "A=B" {
		t.Fatalf("materialized values = %#v", arg.value)
	}
}
