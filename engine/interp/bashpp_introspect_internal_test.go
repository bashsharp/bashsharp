// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"testing"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// B27b: opaque-field redaction below the shell-text surface. Mixed shell text
// cannot declare a channel, function or pointer struct field today — those
// shapes arrive through Go-source units — so the redaction rule for them is
// pinned here at the classifier, where every arrival route converges.

func TestBashPPOpaqueField(t *testing.T) {
	intType := &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "int"}}
	for _, tc := range []struct {
		name   string
		typ    syntax.BashPPTypeExpr
		meta   *bashPPCollectionMeta
		opaque bool
	}{
		{"channel type", &syntax.BashPPChanType{Element: intType}, nil, true},
		{"function type", &syntax.BashPPFuncType{}, nil, true},
		{"pointer type", &syntax.BashPPPointerType{Element: intType}, nil, true},
		{"channel identity in the payload", nil, &bashPPCollectionMeta{channel: &bashPPChannel{}}, true},
		{"plain named type", intType, nil, false},
		{"collection type", &syntax.BashPPCollectionType{Kind: "slice", Element: intType}, nil, false},
	} {
		if got := bashPPOpaqueField(tc.typ, tc.meta); got != tc.opaque {
			t.Errorf("%s: opaque=%v, want %v", tc.name, got, tc.opaque)
		}
	}
}

func TestBashPPDescribeFieldsRedactsOpaqueStorage(t *testing.T) {
	// A record with an exported channel-valued field: the field is listed by
	// name and type, and its value — an internal capability string — is
	// withheld even though the field is public.
	r := &Runner{}
	st := &syntax.BashPPStructType{Fields: []*syntax.BashPPField{
		{Names: []*syntax.Lit{{Value: "Name"}}, FieldTypeExpr: &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "string"}}},
		{Names: []*syntax.Lit{{Value: "Done"}}, FieldTypeExpr: &syntax.BashPPChanType{Element: &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "int"}}}},
	}}
	meta := &bashPPCollectionMeta{kind: "struct", mapping: map[string]*bashPPCollectionMeta{
		"Done": {channel: &bashPPChannel{}},
	}}
	cell := &bashPPCell{
		vr:        expand.NewObject(map[string]any{"Name": "job", "Done": "chan@bashpp:deadbeef"}),
		valueMeta: meta,
	}
	fields := r.bashPPDescribeFields(cell, st)
	if len(fields) != 2 {
		t.Fatalf("fields = %+v; want 2", fields)
	}
	if fields[0] != (ValueField{Name: "Name", Type: "string", Value: "job"}) {
		t.Fatalf("Name field = %+v", fields[0])
	}
	want := ValueField{Name: "Done", Type: "chan int", Redacted: true}
	if fields[1] != want {
		t.Fatalf("Done field = %+v; want %+v (the capability string must not surface)", fields[1], want)
	}
}
