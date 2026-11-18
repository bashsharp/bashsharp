//go:build full

// Sprint: #219; Story: #462; Story-ID: e348d2c13248
package interp

import (
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/go-quicktest/qt"
)

func TestS219BridgeTypeSpelling(t *testing.T) {
	for _, test := range []struct {
		input string
		want  string
	}{
		{"*any", "*any"},
		{"func() any", "func()(any)"},
		{"func(*any) (any, error)", "func(*any)(any,error)"},
		{"map[string][]func(*any) map[any]*interface{}", "map[string][]func(*any)(map[any]*interface{})"},
		{"Named[func() any, *interface{}]", "Named[func()(any),*interface{}]"},
	} {
		t.Run(test.input, func(t *testing.T) {
			typ := syntax.BashPPTypeExprFromText(test.input)
			qt.Assert(t, qt.IsNotNil(typ))
			qt.Assert(t, qt.Equals(bashPPBridgeTypeText(typ), test.want))
		})
	}
	variadic := &syntax.BashPPFuncType{Params: []*syntax.BashPPField{
		{FieldTypeExpr: &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "string"}}},
		{FieldTypeExpr: &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "any"}}, Ellipsis: syntax.NewPos(1, 1, 1)},
	}}
	qt.Assert(t, qt.Equals(bashPPBridgeTypeText(variadic), "func(string,...any)()"))
	nestedVariadic := &syntax.BashPPFuncType{Params: []*syntax.BashPPField{{FieldTypeExpr: &syntax.BashPPFuncType{
		Params: []*syntax.BashPPField{{
			FieldTypeExpr: &syntax.BashPPPointerType{Element: &syntax.BashPPInterfaceType{}},
			Ellipsis:      syntax.NewPos(1, 1, 1),
		}},
		Results: []*syntax.BashPPField{{FieldTypeExpr: &syntax.BashPPFuncType{
			Params:  []*syntax.BashPPField{{FieldTypeExpr: &syntax.BashPPInterfaceType{}, Ellipsis: syntax.NewPos(1, 1, 1)}},
			Results: []*syntax.BashPPField{{FieldTypeExpr: &syntax.BashPPPointerType{Element: &syntax.BashPPInterfaceType{}}}},
		}}},
	}}}}
	qt.Assert(t, qt.Equals(bashPPBridgeTypeText(nestedVariadic), "func(func(...*interface{})(func(...interface{})(*interface{})))()"))

	elemsOnly := &syntax.BashPPInterfaceType{Elems: []*syntax.BashPPInterfaceElem{{Method: &syntax.BashPPMethodSpec{
		Name:    &syntax.Lit{Value: "Apply"},
		Params:  []*syntax.BashPPField{{FieldTypeExpr: &syntax.BashPPPointerType{Element: &syntax.BashPPInterfaceType{}}}},
		Results: []*syntax.BashPPField{{FieldTypeExpr: &syntax.BashPPFuncType{Results: []*syntax.BashPPField{{FieldTypeExpr: &syntax.BashPPInterfaceType{}}}}}},
	}}}}
	qt.Assert(t, qt.Equals(bashPPBridgeTypeText(elemsOnly), "interface{Apply(*interface{})(func()(interface{}))}"))

	// An absent type still follows the old invalid-spelling path, so helper
	// registration rejects it instead of silently authenticating a type guess.
	qt.Assert(t, qt.Equals(bashPPBridgeTypeText(nil), "<inferred>"))
}
