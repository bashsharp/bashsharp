// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"context"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestBashPPCollectionRuntimeIdentity(t *testing.T) {
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(
		"a := [1]int{1}\ns := []int{1}\nm := map[string]int{\"k\": 1}\n"), "identity.bpp")
	if err != nil {
		t.Fatal(err)
	}
	r, err := New(Lang(syntax.LangBashPP))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"a": "array", "s": "slice", "m": "map"} {
		cell := r.bashPPScope.lookup(name)
		if cell == nil || cell.object == nil || cell.object.collection == nil || cell.object.collection.kind != want {
			t.Fatalf("%s identity = %#v, want %s", name, cell, want)
		}
	}
}

func TestBashPPTaskSnapshotClonesCollectionMetadata(t *testing.T) {
	childMeta := &bashPPCollectionMeta{kind: "slice"}
	parentMeta := &bashPPCollectionMeta{kind: "array", sequence: []*bashPPCollectionMeta{childMeta}}
	parentValue := []any{[]any{1}}
	parent := &Runner{bashPPScope: newBashPPScope(nil)}
	identity := &bashPPObjectIdentity{owner: "a", collection: parentMeta}
	parent.bashPPScope.entries["a"] = &bashPPCell{vr: expand.NewObject(parentValue), object: identity}

	child := &Runner{bashPPScope: newBashPPCloner().clone(parent.bashPPScope)}
	if err := cloneBashPPTaskCells(child, newBashPPObjectCloner(), nil); err != nil {
		t.Fatal(err)
	}
	childCell := child.bashPPScope.lookup("a")
	childCell.vr.Obj.([]any)[0].([]any)[0] = 2
	childCell.object.collection.sequence[0] = nil
	if got := parentValue[0].([]any)[0]; got != 1 {
		t.Fatalf("task payload mutation escaped snapshot: %v", got)
	}
	if parentMeta.sequence[0] != childMeta {
		t.Fatal("task metadata mutation escaped snapshot")
	}
}

func TestBashPPSubshellCloneOwnsEmbeddedInterfaceDynamicPointer(t *testing.T) {
	voiceType := &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "Voice"}}
	target := &bashPPCell{vr: expand.Variable{Set: true, Kind: expand.String, Str: "parent"}, declType: voiceType}
	dynamic := &bashPPCell{
		vr:           expand.Variable{Set: true, Kind: expand.String},
		declType:     &syntax.BashPPPointerType{Element: voiceType},
		pointer:      true,
		pointerValue: &bashPPPointer{target: target, elem: voiceType},
	}
	interfaceType := &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "Speaker"}}
	interfaceMeta := &bashPPCollectionMeta{kind: "interface", typ: interfaceType, interfaceValue: &bashPPInterfaceValue{
		dynamic: dynamic.declType,
		cell:    dynamic,
	}}
	structMeta := &bashPPCollectionMeta{
		kind:    "struct",
		typ:     &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "Outer"}},
		mapping: map[string]*bashPPCollectionMeta{"Speaker": interfaceMeta},
	}
	parent := newBashPPScope(nil)
	parent.entries["o"] = &bashPPCell{
		vr:        expand.NewObject(map[string]any{"Speaker": ""}),
		valueMeta: structMeta,
		object:    &bashPPObjectIdentity{collection: structMeta},
	}

	child := newBashPPCloner().clone(parent)
	childInterface := child.lookup("o").valueMeta.mapping["Speaker"].interfaceValue
	if childInterface == nil || childInterface.cell == dynamic {
		t.Fatal("embedded interface dynamic cell escaped subshell clone")
	}
	if childInterface.cell.pointerValue == dynamic.pointerValue || childInterface.cell.pointerValue.target == target {
		t.Fatal("embedded interface dynamic pointer escaped subshell clone")
	}
	childInterface.cell.pointerValue.target.vr.Str = "child"
	if target.vr.Str != "parent" {
		t.Fatalf("embedded interface mutation escaped subshell: %q", target.vr.Str)
	}
}
