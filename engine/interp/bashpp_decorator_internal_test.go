// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"testing"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestBashPPDecoratorTypedNoopIdentity(t *testing.T) {
	target := &bashPPCell{vr: expand.Variable{Set: true, Kind: expand.String, Str: "target"}}
	ptr := &bashPPPointer{target: target}
	channel := &bashPPChannel{}
	iface := &bashPPInterfaceValue{dynamic: &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "int"}}, cell: target}
	sharedMap := map[string]any{"x": 1}

	cells := []*bashPPCell{
		{pointer: true, pointerValue: ptr, declType: &syntax.BashPPPointerType{Element: target.declType}},
		{vr: expand.NewObject(sharedMap), valueMeta: &bashPPCollectionMeta{kind: "map"}},
		{channel: channel, declType: &syntax.BashPPChanType{Element: &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "int"}}}},
		{interfaceValue: iface, declType: &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "any"}}},
		{vr: expand.Variable{Set: true, Kind: expand.String, Str: "function-handle"}, declType: &syntax.BashPPFuncType{}},
	}

	for i, original := range cells {
		got, ok := bashPPDecoratorCellValue(original).(*bashPPCell)
		if !ok {
			t.Fatalf("position %d lost its typed cell", i)
		}
		switch i {
		case 0:
			if got.pointerValue != ptr {
				t.Fatal("pointer identity changed")
			}
		case 1:
			got.vr.Obj.(map[string]any)["x"] = 2
			if sharedMap["x"] != 2 {
				t.Fatal("map identity changed")
			}
		case 2:
			if got.channel != channel {
				t.Fatal("channel identity changed")
			}
		case 3:
			if got.interfaceValue != iface {
				t.Fatal("interface identity changed")
			}
		case 4:
			if got.vr.Str != original.vr.Str {
				t.Fatal("function identity changed")
			}
		}
	}
}
