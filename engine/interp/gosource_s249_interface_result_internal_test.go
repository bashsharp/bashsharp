package interp

import (
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// The package-map/test-main runner can evaluate an imported call as a value,
// outside the assignment fast path. Its concrete wire value still has the
// checked interface result type and exactly one dynamic payload wrapper.
func TestGoSourceS249NativeValueResultKeepsInterfaceWrapper(t *testing.T) {
	r := &Runner{bashPPGoSource: true}
	resultType := &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "error"}}
	call := &syntax.BashPPCall{ResultTypes: []syntax.BashPPTypeExpr{resultType}}
	value := bashPPBridgeValue{Kind: "struct", Type: "go/types.Error", Handle: 1}

	cells := r.goSourceNativeCallResultCells(call, []bashPPBridgeValue{value})
	if len(cells) != 1 || cells[0].interfaceValue == nil {
		t.Fatalf("result = %#v, want interface wrapper", cells)
	}
	if cells[0].declType != resultType {
		t.Fatalf("static type = %v, want checked error type", cells[0].declType)
	}
	payload := cells[0].interfaceValue.cell
	if payload == nil || payload.interfaceValue != nil {
		t.Fatalf("dynamic payload = %#v, want one unwrapped concrete value", payload)
	}
}

func TestGoSourceS281NativeNilInterfacePredicateKeepsDynamicPointer(t *testing.T) {
	if bashPPBridgeValueNilInterface(bashPPBridgeValue{Kind: "nil", Type: "any", Interface: "any", NativeType: "*main.T"}) {
		t.Fatal("typed nil dynamic pointer collapsed to nil interface")
	}
	if !bashPPBridgeValueNilInterface(bashPPBridgeValue{Kind: "nil", Type: "any", Interface: "any"}) {
		t.Fatal("plain nil interface stopped comparing as nil")
	}
}
