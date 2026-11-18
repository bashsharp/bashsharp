//go:build full

package interp

import (
	"go/types"
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestS243AliasSignatureImportIdentity(t *testing.T) {
	named := func(name string) *syntax.BashPPNamedType {
		return &syntax.BashPPNamedType{Name: &syntax.Lit{Value: name}}
	}
	fields := func(name string) []*syntax.BashPPField { return []*syntax.BashPPField{{FieldTypeExpr: named(name)}} }
	r := &Runner{bashPPImports: map[string]string{"a": "example.com/one", "b": "example.com/one", "other": "example.com/two"}, bashPPTypes: map[string]bashPPType{
		"Alias":   {alias: true, typeExpr: named("a.T")},
		"Defined": {typeExpr: named("a.T")},
	}}
	for _, tc := range []struct {
		left, right string
		want        bool
	}{
		{"a.T", "b.T", true},
		{"Alias", "b.T", true},
		{"a.T", "other.T", false},
		{"a.T", "b.U", false},
		{"Defined", "a.T", false},
	} {
		if got := r.bashPPAliasedSignatureEqual(nil, fields(tc.left), nil, fields(tc.right)); got != tc.want {
			t.Errorf("%s vs %s: %v, want %v", tc.left, tc.right, got, tc.want)
		}
	}
	if r.bashPPAliasedSignatureEqual(fields("a.T"), nil, nil, fields("b.T")) {
		t.Fatal("parameter and result roles merged")
	}
}

func TestS319NativeTypePathIdentity(t *testing.T) {
	r := &Runner{bashPPImports: map[string]string{
		"fileA": "go/ast",
		"fileB": "go/ast",
		"other": "example.com/ast",
	}}
	for _, tc := range []struct {
		name string
		want string
		ok   bool
	}{
		{"*fileA.Ident", "*go/ast.Ident", true},
		{"*fileB.Ident", "*go/ast.Ident", true},
		{"*go/ast.Ident", "*go/ast.Ident", true},
		{"*other.Ident", "*example.com/ast.Ident", true},
		{"*ast.Ident", "", false},
		{"Ident", "", false},
	} {
		got, ok := r.goSourceNativeTypePathIdentity(tc.name)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s: got %q, %v; want %q, %v", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}

func TestS281SignatureImportIdentity(t *testing.T) {
	named := func(name string) *syntax.BashPPNamedType {
		return &syntax.BashPPNamedType{Name: &syntax.Lit{Value: name}}
	}
	fields := func(name string) []*syntax.BashPPField {
		return []*syntax.BashPPField{{FieldTypeExpr: named(name)}}
	}
	nativeType := func(path, pkgName, typeName string) types.Type {
		pkg := types.NewPackage(path, pkgName)
		obj := types.NewTypeName(0, pkg, typeName, nil)
		return types.NewNamed(obj, types.NewStruct(nil, nil), nil)
	}

	t.Run("ambiguous declared qualifier refuses", func(t *testing.T) {
		r := &Runner{
			bashPPGoSource: true,
			bashPPImports: map[string]string{
				"first":  "example/a/p",
				"second": "example/b/p",
			},
		}
		r.bashPPTools.nativeTypes = map[string]types.Type{
			"example/a/p.Token": nativeType("example/a/p", "p", "Token"),
			"example/b/p.Token": nativeType("example/b/p", "p", "Token"),
		}
		if got := r.goSourceSignatureImportPath("p", "Token"); got != "" {
			t.Fatalf("ambiguous declared qualifier resolved to %q", got)
		}
		if r.bashPPAliasedSignatureEqual(nil, fields("p.Token"), nil, fields("first.Token")) {
			t.Fatal("ambiguous declared qualifier matched an imported package")
		}
	})

	t.Run("declared package name from versioned path", func(t *testing.T) {
		const path = "example/versioned/v2"
		r := &Runner{
			bashPPGoSource: true,
			bashPPImports:  map[string]string{"synthetic": path},
		}
		r.bashPPTools.nativeTypes = map[string]types.Type{
			path + ".Token": nativeType(path, "actualpkg", "Token"),
		}
		if got := r.goSourceSignatureImportPath("actualpkg", "Token"); got != path {
			t.Fatalf("declared package identity got %q", got)
		}
	})

	t.Run("renamed import and declared package name share identity", func(t *testing.T) {
		const path = "example/versioned/v2"
		r := &Runner{
			bashPPGoSource: true,
			bashPPImports:  map[string]string{"renamed": path},
		}
		r.bashPPTools.nativeTypes = map[string]types.Type{
			path + ".Token": nativeType(path, "actualpkg", "Token"),
		}
		if got := r.goSourceSignatureImportPath("renamed", "Token"); got != path {
			t.Fatalf("renamed import got %q", got)
		}
		if !r.bashPPAliasedSignatureEqual(nil, fields("renamed.Token"), nil, fields("actualpkg.Token")) {
			t.Fatal("renamed import and declared package name did not match")
		}
	})
}
