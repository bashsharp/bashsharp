//go:build full

package interp

import (
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestS243NativeComplexCollectionCarrier(t *testing.T) {
	r := &Runner{bashPPGoSource: true}
	typ := &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "complex128"}}
	value, meta, err := r.bashPPBridgeContents(bashPPBridgeValue{
		Kind: "complex", Type: "complex128", Text: "(5+7i)",
	}, typ)
	if err != nil || meta != nil || value != "(5+7i)" {
		t.Fatalf("value=%#v meta=%#v err=%v", value, meta, err)
	}
	if err := r.bashPPCheckCollectionValue(value, typ); err != nil {
		t.Fatalf("complex carrier rejected at declared destination: %v", err)
	}
}

func TestS243NativeComplexCarrierRejectsMalformedText(t *testing.T) {
	for _, text := range []string{"not-a-number", "(1+oops)", "(1+2i)junk"} {
		if _, _, err := bashPPBridgeScalarValue(bashPPBridgeValue{Kind: "complex", Type: "complex128", Text: text}); err == nil {
			t.Fatalf("accepted malformed complex payload %q", text)
		}
	}
}

func TestS243NativeNumericTextKeepsStringType(t *testing.T) {
	r := &Runner{bashPPGoSource: true}
	typ := &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "string"}}
	for _, text := range []string{"(5+7i)", "18446744073709551615", "NaN"} {
		got, err := r.bashPPBridgeCollection(text, nil, typ)
		if err != nil || got.Kind != "string" || got.Type != "string" || got.Text != text {
			t.Fatalf("numeric-looking string %q coerced: %+v, %v", text, got, err)
		}
	}
}
