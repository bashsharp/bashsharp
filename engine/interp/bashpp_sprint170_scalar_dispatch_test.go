// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestSprint170BridgeDefinedScalarUnsignedBoundary(t *testing.T) {
	r := &Runner{bashPPTypes: map[string]bashPPType{
		"Word": {typeExpr: &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "uint64"}}},
	}}

	got, err := r.bashPPBridgeDefinedScalar(bashPPBridgeValue{Kind: "int", Type: "Word", Text: "9223372036854775811"})
	if err != nil || got.Kind != "uint" {
		t.Fatalf("wide uint64: got %+v, err %v", got, err)
	}

	for _, text := range []string{"-1", "18446744073709551616"} {
		if _, err := r.bashPPBridgeDefinedScalar(bashPPBridgeValue{Kind: "int", Type: "Word", Text: text}); err == nil {
			t.Fatalf("unsigned boundary %q accepted", text)
		}
	}

	plain := bashPPBridgeValue{Kind: "int", Type: "external.Count", Text: "7"}
	got, err = r.bashPPBridgeDefinedScalar(plain)
	if err != nil || got.Kind != plain.Kind || got.Type != plain.Type || got.Text != plain.Text {
		t.Fatalf("unknown type changed: got %+v, err %v", got, err)
	}
}
