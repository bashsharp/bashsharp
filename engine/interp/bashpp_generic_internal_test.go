// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestBashPPConcreteTypeArgumentsRejectTypeSets(t *testing.T) {
	name := func(value string) *syntax.BashPPNamedType {
		return &syntax.BashPPNamedType{Name: &syntax.Lit{Value: value}}
	}
	for _, typ := range []syntax.BashPPTypeExpr{
		&syntax.BashPPApproxType{Term: name("int")},
		&syntax.BashPPUnionType{Terms: []syntax.BashPPTypeExpr{name("int"), name("string")}},
		&syntax.BashPPCollectionType{Kind: "slice", Element: &syntax.BashPPApproxType{Term: name("int")}},
	} {
		err := bashPPValidateConcreteTypeArgs([]*syntax.BashPPTypeArg{{ArgType: typ}})
		if err == nil || !strings.Contains(err.Error(), "BASHPP-EGENERIC-ARG:") {
			t.Fatalf("argument %T error = %v", typ, err)
		}
	}
}
