//go:build full

package interp

import (
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestS219TypeResolutionDirectMatcherNamedTerm(t *testing.T) {
	named := func(name string) *syntax.BashPPNamedType {
		return &syntax.BashPPNamedType{Name: &syntax.Lit{Value: name}}
	}
	r := &Runner{bashPPTypes: map[string]bashPPType{
		"MyInt": {typeExpr: named("int")},
	}}
	if !r.bashPPTypeSetSatisfied(named("MyInt"), named("MyInt")) {
		t.Fatal("named term rejected its exact named type")
	}
	if r.bashPPTypeSetSatisfied(named("int"), named("MyInt")) {
		t.Fatal("named term admitted its unnamed underlying type")
	}
	if r.bashPPConstraintSatisfied(named("int"), named("MyInt")) {
		t.Fatal("named constraint admitted its unnamed underlying type")
	}
}

func TestS219TypeResolutionDirectMatcherNamedComparable(t *testing.T) {
	named := func(name string) *syntax.BashPPNamedType {
		return &syntax.BashPPNamedType{Name: &syntax.Lit{Value: name}}
	}
	r := &Runner{bashPPTypes: map[string]bashPPType{
		"Comparable":      {typeExpr: named("comparable")},
		"ComparableChain": {typeExpr: named("Comparable")},
		"CycleA":          {typeExpr: named("CycleB")},
		"CycleB":          {typeExpr: named("CycleA")},
	}}
	if !r.bashPPConstraintSatisfied(named("int"), named("ComparableChain")) {
		t.Fatal("named comparable chain rejected int")
	}
	if r.bashPPConstraintSatisfied(named("int"), named("CycleA")) {
		t.Fatal("cyclic named constraint admitted int")
	}
}
