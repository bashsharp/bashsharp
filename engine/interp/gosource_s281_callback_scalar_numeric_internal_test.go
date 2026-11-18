//go:build full

package interp

// Sprint: #281; Story: #809; Story-ID: fac7e14af4a8

import (
	"testing"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestS281CallbackScalarNumericComparison(t *testing.T) {
	r := &Runner{bashPPGoSource: true}
	floatMeta := &bashPPCollectionMeta{
		kind: "scalar",
		typ:  &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "float64"}},
	}
	for _, test := range []struct {
		name                string
		left, right         any
		leftMeta, rightMeta *bashPPCollectionMeta
	}{
		{name: "field left", left: float64(1), right: int(1), leftMeta: floatMeta},
		{name: "field right", left: int(1), right: float64(1), rightMeta: floatMeta},
		{name: "unequal", left: float64(1), right: int(2), leftMeta: floatMeta},
	} {
		t.Run(test.name, func(t *testing.T) {
			equal, err := r.bashPPCompareValues(test.left, test.leftMeta, false, test.right, test.rightMeta, false)
			if err != nil {
				t.Fatal(err)
			}
			if equal != (test.name != "unequal") {
				t.Fatalf("equal = %v", equal)
			}
		})
	}

	intMeta := &bashPPCollectionMeta{
		kind: "scalar",
		typ:  &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "int"}},
	}
	if _, err := r.bashPPCompareValues(float64(1), floatMeta, false, int(1), intMeta, false); err == nil {
		t.Fatal("declared float64 and int values compared without a type error")
	}
}

func TestS281BridgeScalarNativeReadCompareChain(t *testing.T) {
	r := &Runner{bashPPGoSource: true}
	native := bashPPBridgeValue{Kind: "string", Type: "string", Text: "_"}
	value, meta, err := bashPPNativeReadValue(native)
	if err != nil {
		t.Fatal(err)
	}
	if meta == nil || meta.kind != "bridge-scalar" || bashPPTypeText(meta.typ) != "string" {
		t.Fatalf("native read meta = %#v", meta)
	}
	equal, err := r.bashPPCompareValues(value, meta, false, "_", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if !equal {
		t.Fatal("native bridge scalar metadata did not compare equal to matching literal")
	}
	equal, err = r.bashPPCompareValues(value, meta, false, "x", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if equal {
		t.Fatal("native bridge scalar metadata compared equal to different literal")
	}

	cell := r.goSourceCollectionReadCell(&syntax.BashPPBasicLit{Kind: "STRING"}, value, meta)
	if cell.vr.Kind != expand.String || cell.vr.Str != "_" {
		t.Fatalf("stored cell = kind %v text %q, want string _", cell.vr.Kind, cell.vr.Str)
	}
	if cell.valueMeta != nil || cell.object != nil {
		t.Fatalf("bridge scalar stored structured metadata: valueMeta=%#v object=%#v", cell.valueMeta, cell.object)
	}
	read, readMeta, err := r.bashPPReadCellValue(cell)
	if err != nil {
		t.Fatal(err)
	}
	if readMeta != nil || read != "_" {
		t.Fatalf("read cell = (%#v, %#v), want (_, nil)", read, readMeta)
	}
	equal, err = r.bashPPCompareValues(read, readMeta, false, "_", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if !equal {
		t.Fatal("native bridge scalar did not compare equal to matching literal")
	}
	equal, err = r.bashPPCompareValues(read, readMeta, false, "x", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if equal {
		t.Fatal("native bridge scalar compared equal to different literal")
	}
}

func TestS281BridgeScalarDeclaredNumericMismatch(t *testing.T) {
	r := &Runner{bashPPGoSource: true}
	floatValue, floatMeta, err := bashPPNativeReadValue(bashPPBridgeValue{Kind: "float", Type: "float64", Text: "1"})
	if err != nil {
		t.Fatal(err)
	}
	intValue, intMeta, err := bashPPNativeReadValue(bashPPBridgeValue{Kind: "int", Type: "int", Text: "1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.bashPPCompareValues(floatValue, floatMeta, false, intValue, intMeta, false); err == nil {
		t.Fatal("declared float64 bridge scalar and int bridge scalar compared without a type error")
	}
}

func TestS281BridgeScalarInterfaceDynamicCell(t *testing.T) {
	r := &Runner{bashPPGoSource: true}
	value, meta, err := bashPPNativeReadValue(bashPPBridgeValue{Kind: "string", Type: "string", Interface: "any", Text: "_"})
	if err != nil {
		t.Fatal(err)
	}
	cell := r.goSourceCollectionReadCell(&syntax.BashPPBasicLit{Kind: "STRING"}, value, meta)
	stringType := &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "string"}}
	iv := &bashPPInterfaceValue{dynamic: stringType, cell: cell}

	equal, handled, err := r.goSourceInterfaceEqual(
		bashPPComparableValue{value: iv},
		bashPPComparableValue{value: "_", meta: meta},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !handled || !equal {
		t.Fatalf("interface bridge scalar comparison handled=%v equal=%v", handled, equal)
	}
	if bashPPTypeText(iv.dynamic) != "string" || bashPPTypeText(iv.cell.declType) != "string" {
		t.Fatalf("interface dynamic/cell types = %s/%s, want string/string", bashPPTypeText(iv.dynamic), bashPPTypeText(iv.cell.declType))
	}
}
