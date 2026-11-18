// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"go/constant"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// The wide-integer fallback is confined to GoSource; Classic retains its
// signed scalar carrier and its established rejection of this conversion.
func TestSprint165WideIntegerClassicParityNegative(t *testing.T) {
	wide := constant.MakeFromLiteral("9223372036854775808", token.INT, 0)
	_, err := (&Runner{}).bashPPConvertScalar("string", bashPPScalar{value: wide})
	if err == nil || !strings.Contains(err.Error(), "cannot convert 9223372036854775808 to string") {
		t.Fatalf("Classic conversion changed: %v", err)
	}
}

func TestSprint165StringToUint64NegativeSet(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "sprint165", "string-to-uint64", "negative.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 4 {
			t.Fatalf("bad negative row %q", line)
		}
		name, sourceType, text, want := fields[0], fields[1], fields[2], fields[3]
		t.Run(name, func(t *testing.T) {
			got, handled, err := (&Runner{bashPPGoSource: true}).bashPPConvertGoSourceStringCarrier("uint64", bashPPScalar{
				value:   constant.MakeString(text),
				typ:     sourceType,
				runtime: true,
			})
			if want == "unhandled" {
				if handled || err != nil || got.value != nil {
					t.Fatalf("handled=%v value=%v err=%v, want unhandled", handled, got.value, err)
				}
				return
			}
			if !handled || err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("handled=%v err=%v, want %q", handled, err, want)
			}
		})
	}
}

func TestS281ImportedScalarStringCarrierIdentity(t *testing.T) {
	named := func(path, packageName, typeName string, underlying types.Type) types.Type {
		pkg := types.NewPackage(path, packageName)
		obj := types.NewTypeName(token.NoPos, pkg, typeName, nil)
		return types.NewNamed(obj, underlying, nil)
	}
	runner := func(imports map[string]string, nativeTypes map[string]types.Type) *Runner {
		return &Runner{
			bashPPGoSource: true,
			bashPPImports:  imports,
			bashPPTools:    bashPPToolchain{nativeTypes: nativeTypes},
		}
	}
	carrier := func(typ, text string) bashPPScalar {
		return bashPPScalar{value: constant.MakeString(text), typ: typ, runtime: true}
	}

	tokenPos := named("go/token", "token", "Pos", types.Typ[types.Int])
	r := runner(map[string]string{"tok": "go/token"}, map[string]types.Type{"go/token.Pos": tokenPos})
	got, handled, err := r.bashPPConvertGoSourceStringCarrier("uint64", carrier("token.Pos", "7"))
	if err != nil || !handled || got.value == nil || constant.ToInt(got.value).String() != "7" {
		t.Fatalf("token.Pos carrier handled=%v got=%v err=%v", handled, got.value, err)
	}

	got, handled, err = r.bashPPConvertGoSourceStringCarrier("int", carrier("float64", "3.75"))
	if err != nil || !handled || got.value == nil || constant.ToInt(got.value).String() != "3" {
		t.Fatalf("float carrier to integer handled=%v got=%v err=%v", handled, got.value, err)
	}

	got, handled, err = r.bashPPConvertGoSourceStringCarrier("float64", carrier("float64", "-0"))
	if handled || err != nil || got.value != nil {
		t.Fatalf("float carrier to float handled=%v got=%v err=%v, want unhandled", handled, got.value, err)
	}

	for _, typ := range []string{"*token.Pos", "string"} {
		got, handled, err := r.bashPPConvertGoSourceStringCarrier("uint64", carrier(typ, "7"))
		if handled || err != nil || got.value != nil {
			t.Fatalf("%s carrier handled=%v got=%v err=%v, want refused", typ, handled, got.value, err)
		}
	}

	invalid := runner(map[string]string{"token": "go/token"}, map[string]types.Type{
		"example.com/not-token.Pos": named("example.com/not-token", "token", "Pos", types.Typ[types.Int]),
	})
	if _, handled, err := invalid.bashPPConvertGoSourceStringCarrier("uint64", carrier("token.Pos", "7")); handled || err != nil {
		t.Fatalf("invalid package identity handled=%v err=%v, want refused", handled, err)
	}

	ambiguous := runner(map[string]string{"one": "example.com/one/token", "two": "example.com/two/token"}, map[string]types.Type{
		"example.com/one/token.Pos": named("example.com/one/token", "token", "Pos", types.Typ[types.Int]),
		"example.com/two/token.Pos": named("example.com/two/token", "token", "Pos", types.Typ[types.Int]),
	})
	if _, handled, err := ambiguous.bashPPConvertGoSourceStringCarrier("uint64", carrier("token.Pos", "7")); handled || err != nil {
		t.Fatalf("ambiguous package name handled=%v err=%v, want refused", handled, err)
	}
}

func TestS281LocalImportedScalarUnderlying(t *testing.T) {
	pkg := types.NewPackage("go/token", "token")
	obj := types.NewTypeName(token.NoPos, pkg, "Pos", nil)
	tokenPos := types.NewNamed(obj, types.Typ[types.Int], nil)
	named := func(name string) *syntax.BashPPNamedType {
		return &syntax.BashPPNamedType{Name: &syntax.Lit{Value: name}}
	}
	r := &Runner{
		bashPPGoSource: true,
		bashPPImports:  map[string]string{"tok": "go/token"},
		bashPPTools:    bashPPToolchain{nativeTypes: map[string]types.Type{"go/token.Pos": tokenPos}},
		bashPPTypes: map[string]bashPPType{
			"atPos":      {underlying: "tok.Pos", typeExpr: named("tok.Pos")},
			"ordinary":   {underlying: "string", typeExpr: named("string")},
			"posPointer": {underlying: "*tok.Pos", typeExpr: &syntax.BashPPPointerType{Element: named("tok.Pos")}},
		},
	}

	if got, ok := r.goSourceScalarUnderlying(named("atPos")); !ok || got != "int" {
		t.Fatalf("atPos underlying = %q, %v; want int, true", got, ok)
	}
	if got, ok := r.goSourceScalarUnderlying(named("ordinary")); !ok || got != "string" {
		t.Fatalf("ordinary underlying = %q, %v; want string, true", got, ok)
	}
	if got, ok := r.goSourceScalarUnderlying(named("posPointer")); ok || got != "" {
		t.Fatalf("posPointer underlying = %q, %v; want empty, false", got, ok)
	}
}

func TestS281BridgeScalarStoreRemainsScalar(t *testing.T) {
	r := &Runner{bashPPGoSource: true}
	named := &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "localInt"}}
	r.bashPPTypes = map[string]bashPPType{
		"localInt": {underlying: "int", typeExpr: named},
	}
	cell := &bashPPCell{declType: named}

	bashPPStoreCellValue(cell, 7, &bashPPCollectionMeta{kind: "scalar", typ: named})
	if cell.vr.Kind != expand.String {
		t.Fatalf("scalar bridge store kind = %v, want string", cell.vr.Kind)
	}
	if cell.declType != named || cell.typeName != "localInt" || cell.scalarKind != constant.Int {
		t.Fatalf("scalar metadata = decl %v type %q kind %v", cell.declType, cell.typeName, cell.scalarKind)
	}
	scalar := r.bashPPScalarFromCell(cell)
	if scalar.typ != "localInt" || constant.ToInt(scalar.value).String() != "7" {
		t.Fatalf("scalar read = (%v, %q), want (7, localInt)", scalar.value, scalar.typ)
	}
}
