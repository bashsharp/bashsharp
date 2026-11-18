//go:build full

package interp

import (
	"go/token"
	"go/types"
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Sprint: #319; Story: #1089; Story-ID: 3185346bc4b5

func TestS319ImportedScalarMethodTypeAuthentication(t *testing.T) {
	pkg := types.NewPackage("go/token", "token")
	posObj := types.NewTypeName(token.NoPos, pkg, "Pos", nil)
	pos := types.NewNamed(posObj, types.Typ[types.Int], nil)
	recv := types.NewVar(token.NoPos, pkg, "", pos)
	result := types.NewVar(token.NoPos, pkg, "", types.Typ[types.Bool])
	pos.AddMethod(types.NewFunc(token.NoPos, pkg, "IsValid", types.NewSignatureType(recv, nil, nil, nil, types.NewTuple(result), false)))
	tokenObj := types.NewTypeName(token.NoPos, pkg, "Token", nil)
	tokenType := types.NewNamed(tokenObj, types.Typ[types.Int], nil)
	named := func(name string) *syntax.BashPPNamedType {
		return &syntax.BashPPNamedType{Name: &syntax.Lit{Value: name}}
	}
	r := &Runner{
		bashPPGoSource: true,
		bashPPImports:  map[string]string{"tok": "go/token"},
		bashPPTools: bashPPToolchain{nativeTypes: map[string]types.Type{
			"go/token.Pos":   pos,
			"go/token.Token": tokenType,
		}},
		bashPPTypes: map[string]bashPPType{
			"aliasPos":   {alias: true, typeExpr: named("tok.Pos")},
			"definedPos": {typeExpr: named("tok.Pos")},
			"localInt":   {typeExpr: named("int")},
		},
	}

	for _, test := range []struct {
		name   string
		typ    string
		method string
		want   bool
	}{
		{name: "direct imported", typ: "tok.Pos", method: "IsValid", want: true},
		{name: "alias imported", typ: "aliasPos", method: "IsValid", want: true},
		{name: "defined from imported", typ: "definedPos", method: "IsValid"},
		{name: "same representation", typ: "localInt", method: "IsValid"},
		{name: "wrong imported type", typ: "tok.Token", method: "IsValid"},
		{name: "wrong method", typ: "tok.Pos", method: "Missing"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, ok := r.goSourceImportedScalarMethodType(named(test.typ), test.method)
			if ok != test.want {
				t.Fatalf("resolved %s.%s as %s, %v; want handled %v", test.typ, test.method, bashPPTypeText(got), ok, test.want)
			}
		})
	}
}
