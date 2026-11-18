package interp

import (
	"go/token"
	"testing"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestGoSourceNilAggregateClassicBoundary(t *testing.T) {
	meta := &bashPPCollectionMeta{kind: "interface", interfaceValue: &bashPPInterfaceValue{nilIface: true}}
	// Classic comparison continues to use its original printable payload.
	equal, err := bashPPCompareValues("", meta, false, nil, nil, true)
	if err != nil || equal {
		t.Fatalf("classic changed: equal=%v err=%v", equal, err)
	}
	for _, goSource := range []bool{false, true} {
		r := &Runner{bashPPGoSource: goSource, bashPPScope: newBashPPScope(nil)}
		shape := &syntax.BashPPStructType{Fields: []*syntax.BashPPField{{Names: []*syntax.Lit{{Value: "I"}}, FieldTypeExpr: &syntax.BashPPInterfaceType{}}}}
		cell := &bashPPCell{declType: shape}
		bashPPStoreCellValue(cell, map[string]any{"I": ""}, &bashPPCollectionMeta{kind: "struct", typ: shape, mapping: map[string]*bashPPCollectionMeta{"I": meta}})
		r.bashPPScope.entries["box"] = cell
		field := &syntax.BashPPSelectorExpr{X: &syntax.BashPPIdent{Name: &syntax.Lit{Value: "box"}}, Sel: &syntax.Lit{Value: "I"}}
		nilExpr := &syntax.BashPPIdent{Name: &syntax.Lit{Value: "nil"}}
		got, err := r.bashPPCompareExpr(field, token.EQL, nilExpr)
		if err != nil || got != goSource {
			t.Fatalf("runtime mode GoSource=%v: equal=%v err=%v", goSource, got, err)
		}
	}
	equal, err = bashPPCompareValues(bashPPComparablePayload("", meta), meta, false, nil, nil, true)
	if err != nil || !equal {
		t.Fatalf("Go metadata lost: equal=%v err=%v", equal, err)
	}
}

func TestGoSourceTypedNilNativeInterfaceFieldCompare(t *testing.T) {
	r := &Runner{bashPPGoSource: true, bashPPScope: newBashPPScope(nil)}
	shape := &syntax.BashPPStructType{Fields: []*syntax.BashPPField{{Names: []*syntax.Lit{{Value: "I"}}, FieldTypeExpr: &syntax.BashPPInterfaceType{}}}}
	dynamic := &syntax.BashPPPointerType{Element: &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "T"}}}
	native := &bashPPBridgeValue{Kind: "nil", Type: "any", Interface: "any", NativeType: "*main.T"}
	typedNil := &bashPPInterfaceValue{
		dynamic: dynamic,
		cell:    &bashPPCell{vr: expand.NewObject(native), declType: dynamic, typeName: "*main.T"},
	}
	cell := &bashPPCell{declType: shape}
	bashPPStoreCellValue(cell, map[string]any{"I": ""}, &bashPPCollectionMeta{kind: "struct", typ: shape, mapping: map[string]*bashPPCollectionMeta{
		"I": {kind: "interface", typ: &syntax.BashPPInterfaceType{}, interfaceValue: typedNil},
	}})
	r.bashPPScope.entries["box"] = cell

	field := &syntax.BashPPSelectorExpr{X: &syntax.BashPPIdent{Name: &syntax.Lit{Value: "box"}}, Sel: &syntax.Lit{Value: "I"}}
	nilExpr := &syntax.BashPPIdent{Name: &syntax.Lit{Value: "nil"}}
	got, err := r.bashPPCompareExpr(field, token.EQL, nilExpr)
	if err != nil || got {
		t.Fatalf("typed nil native interface field compared as nil: equal=%v err=%v", got, err)
	}
}

func TestGoSourceNilAggregateRejectInvalidElements(t *testing.T) {
	r := &Runner{bashPPGoSource: true}
	nilExpr := &syntax.BashPPIdent{Name: &syntax.Lit{Value: "nil"}}
	integer := &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "int"}}
	for _, typ := range []syntax.BashPPTypeExpr{integer, &syntax.BashPPStructType{}, &syntax.BashPPCollectionType{Kind: "array", Length: &syntax.Lit{Value: "2"}, Element: integer}} {
		if _, _, handled, err := r.goSourceNilElement(nilExpr, typ); !handled || err == nil {
			t.Fatalf("nil accepted as %s: handled=%v err=%v", bashPPTypeText(typ), handled, err)
		}
	}
	r.bashPPGoSource = false
	if _, _, handled, err := r.goSourceNilElement(nilExpr, integer); handled || err != nil {
		t.Fatalf("classic intercepted: %v %v", handled, err)
	}
}
