//go:build full

package interp

import (
	"go/types"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Sprint: #319; Story: #1089; Story-ID: 3185346bc4b5
//
// A dependency method may spell a result through its declared package name
// while a mapped package's interface spells that same result through the
// linker's per-package alias. This is the assignment performed by go/types'
// defaultImporter: go/importer.gcimports implements go/types.Importer through
// Import(string) (*go/types.Package, error).
func TestGoSourceS319MappedPackageNativeMethodSignature(t *testing.T) {
	program, err := gosource.Load([]gosource.Source{{Name: "main.go", Data: []byte(`package main
import . "go/types"
func main() { var _ Importer }
`)}}, gosource.Options{
		RunMain:    true,
		ImportPath: "go/types.test",
		Packages: []gosource.PackageSpec{{Path: "go/types", Sources: []gosource.Source{{
			Name: "api.go", Data: []byte(`package types
type Package struct{}
type Importer interface { Import(string) (*Package, error) }
`),
		}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var spec *syntax.BashPPMethodSpec
	for _, stmt := range program.File.Stmts {
		decl, ok := stmt.Cmd.(*syntax.BashPPDecl)
		if !ok || decl.Name == nil || goSourceDeclaredName(decl.Name.Value) != "Importer" {
			continue
		}
		iface, ok := decl.DeclTypeExpr.(*syntax.BashPPInterfaceType)
		if !ok || len(iface.Methods) != 1 {
			t.Fatalf("Importer declaration has type %T", decl.DeclTypeExpr)
		}
		spec = iface.Methods[0]
	}
	if spec == nil {
		t.Fatal("mapped Importer declaration not found")
	}

	nativePackage := func(path, packageName string) types.Type {
		pkg := types.NewPackage(path, packageName)
		obj := types.NewTypeName(0, pkg, "Package", nil)
		return types.NewNamed(obj, types.NewStruct(nil, nil), nil)
	}
	r := &Runner{
		bashPPGoSource:     true,
		bashPPGoSourceFile: program.File,
		bashPPImports:      map[string]string{"types": "go/types", "other": "example/other"},
	}
	r.bashPPTools.nativeTypes = map[string]types.Type{
		"go/types.Package":      nativePackage("go/types", "types"),
		"example/other.Package": nativePackage("example/other", "other"),
	}
	method := bashPPInterfaceMethod{spec: spec, sig: bashPPMethodSpecSignature(spec), name: "Import"}
	if !r.goSourceNativeSignatureMatches("func(string) (*types.Package, error)", "string->*go/types.Package,error", method) {
		t.Fatal("authenticated go/types.Package result did not match its mapped-package alias")
	}
	for _, wrong := range []struct{ text, identity string }{
		{"func(string) (*other.Package, error)", "string->*example/other.Package,error"},
		{"func(string) (types.Package, error)", "string->go/types.Package,error"},
		{"func(string) (*types.Package, string)", "string->*go/types.Package,string"},
	} {
		if r.goSourceNativeSignatureMatches(wrong.text, wrong.identity, method) {
			t.Errorf("mismatched signature %q was accepted", wrong.text)
		}
	}
}

// Sprint: #319; Story: #1084; Story-ID: 7decf01bdd39
//
// A mapped package passes *testing.T to testing.TB before every test body.
// Reflection spells T.Error's variadic empty-interface parameter as
// "interface {}", while go/types preserves the source alias "any" in TB's
// method specification. They are the same predeclared type and must compare
// equal in the authenticated signature path.
func TestGoSourceS319MappedReceiverAnySignature(t *testing.T) {
	empty := types.NewInterfaceType(nil, nil).Complete()
	params := types.NewTuple(types.NewVar(0, nil, "args", types.NewSlice(empty)))
	sig := types.NewSignatureType(nil, nil, nil, params, nil, true)
	spec := bashPPImportedMethodSpec("Error", sig)
	method := bashPPInterfaceMethod{spec: spec, sig: bashPPMethodSpecSignature(spec), name: "Error"}
	r := &Runner{bashPPGoSource: true}

	if !r.goSourceNativeSignatureMatches("func(...interface {})", "...interface {}->", method) {
		t.Fatalf("*testing.T.Error authenticated signature did not match testing.TB.Error: native=%q mapped=%q canonical=%q", "...interface {}->", method.sig, r.bashPPCanonicalSignatureFields(spec.Params)+"->"+r.bashPPCanonicalSignatureFields(spec.Results))
	}

	shadowSpec := &syntax.BashPPMethodSpec{
		Name: &syntax.Lit{Value: "Error"},
		Params: []*syntax.BashPPField{{
			FieldTypeExpr: &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "any"}},
			Ellipsis:      syntax.NewPos(0, 1, 1),
		}},
	}
	shadow := &Runner{bashPPGoSource: true, bashPPTypes: map[string]bashPPType{
		"any": {underlying: "string", typeExpr: &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "any"}}},
	}}
	shadowMethod := bashPPInterfaceMethod{spec: shadowSpec, sig: bashPPMethodSpecSignature(shadowSpec), name: "Error"}
	if shadow.goSourceNativeSignatureMatches("func(...interface {})", "...interface {}->", shadowMethod) {
		t.Fatal("a declared type named any was confused with the predeclared empty-interface alias")
	}
}
