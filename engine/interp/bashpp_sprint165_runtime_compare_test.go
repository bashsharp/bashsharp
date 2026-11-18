package interp

import "testing"

// Only an untyped scalar constant facing a typed scalar is retyped; typed
// operands, handles, interface-carrying values and nils are never rewritten.
func TestSprint165BridgeCompareOperands(t *testing.T) {
	same := func(a, b bashPPBridgeValue) bool {
		return a.Kind == b.Kind && a.Type == b.Type && a.Text == b.Text && a.Interface == b.Interface && a.Handle == b.Handle
	}
	float := bashPPBridgeValue{Kind: "float", Type: "float64", Text: "8"}
	untyped := bashPPBridgeValue{Kind: "int", Text: "8"}
	l, r := bashPPBridgeCompareOperands(float, untyped)
	if !same(l, float) || r.Type != "float64" || r.Kind != "int" {
		t.Fatalf("untyped right operand not retyped: %+v %+v", l, r)
	}
	l, r = bashPPBridgeCompareOperands(untyped, float)
	if !same(r, float) || l.Type != "float64" {
		t.Fatalf("untyped left operand not retyped: %+v %+v", l, r)
	}
	typed := bashPPBridgeValue{Kind: "int", Type: "int", Text: "8"}
	if l, r := bashPPBridgeCompareOperands(float, typed); !same(l, float) || !same(r, typed) {
		t.Fatalf("typed operands rewritten: %+v %+v", l, r)
	}
	if l, r := bashPPBridgeCompareOperands(untyped, untyped); !same(l, untyped) || !same(r, untyped) {
		t.Fatalf("untyped pair rewritten: %+v %+v", l, r)
	}
	handle := bashPPBridgeValue{Kind: "handle", Type: "bytes.Buffer", Handle: 1}
	if l, r := bashPPBridgeCompareOperands(handle, untyped); !same(l, handle) || !same(r, untyped) {
		t.Fatalf("handle comparison rewritten: %+v %+v", l, r)
	}
	iface := bashPPBridgeValue{Kind: "string", Type: "string", Interface: "any", Text: "x"}
	if l, r := bashPPBridgeCompareOperands(iface, bashPPBridgeValue{Kind: "string", Text: "x"}); !same(l, iface) || r.Type != "" {
		t.Fatalf("interface-carrying comparison rewritten: %+v %+v", l, r)
	}
	nilValue := bashPPBridgeValue{Kind: "nil", Type: "*int"}
	if l, r := bashPPBridgeCompareOperands(nilValue, untyped); !same(l, nilValue) || !same(r, untyped) {
		t.Fatalf("nil comparison rewritten: %+v %+v", l, r)
	}
}
