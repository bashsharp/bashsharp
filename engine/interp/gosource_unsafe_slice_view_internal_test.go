package interp

import (
	"go/token"
	"go/types"
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestGoSourceUnsafeSliceHeaderViewUsesImportedStructShape(t *testing.T) {
	pkg := types.NewPackage("internal/unsafeheader", "unsafeheader")
	header := types.NewNamed(types.NewTypeName(token.NoPos, pkg, "SliceHeader", nil), types.NewStruct([]*types.Var{
		types.NewField(token.NoPos, pkg, "Data", types.Typ[types.UnsafePointer], false),
		types.NewField(token.NoPos, pkg, "Len", types.Typ[types.Int], false),
		types.NewField(token.NoPos, pkg, "Cap", types.Typ[types.Int], false),
	}, nil), nil)
	r := &Runner{
		bashPPGoSource: true,
		bashPPImports:  map[string]string{"unsafeheader": "internal/unsafeheader", "unsafe": "unsafe"},
		bashPPTools:    bashPPToolchain{nativeTypes: map[string]types.Type{}},
	}
	r.bashPPTools.nativeTypes["internal/unsafeheader.Slice"] = header
	source := &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "unsafeheader.Slice"}}
	target := &syntax.BashPPCollectionType{
		Kind:    "slice",
		Element: &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "int32"}},
	}
	if view := r.goSourceUnsafeSliceHeaderView(source, target); view == nil {
		t.Fatal("authenticated imported slice header was not recognized")
	}
	fields, ok := r.goSourceUnsafeSliceHeaderFields(source)
	if !ok || len(bashPPFlatFields(fields)) != 3 {
		t.Fatalf("imported slice header fields = %#v, %v; want authenticated local shape", fields, ok)
	}
}

// A file may import two packages that each export a type named Slice:
// cmd/compile/internal/ssa imports internal/unsafeheader for the header and
// cmd/compile/internal/types for its own Slice. The qualifier written in the
// source selects the package; the bare name alone is ambiguous.
func TestGoSourceUnsafeSliceHeaderViewPicksQualifiedPackage(t *testing.T) {
	headerPkg := types.NewPackage("internal/unsafeheader", "unsafeheader")
	header := types.NewNamed(types.NewTypeName(token.NoPos, headerPkg, "Slice", nil), types.NewStruct([]*types.Var{
		types.NewField(token.NoPos, headerPkg, "Data", types.Typ[types.UnsafePointer], false),
		types.NewField(token.NoPos, headerPkg, "Len", types.Typ[types.Int], false),
		types.NewField(token.NoPos, headerPkg, "Cap", types.Typ[types.Int], false),
	}, nil), nil)
	otherPkg := types.NewPackage("cmd/compile/internal/types", "types")
	other := types.NewNamed(types.NewTypeName(token.NoPos, otherPkg, "Slice", nil), types.NewStruct([]*types.Var{
		types.NewField(token.NoPos, otherPkg, "Elem", types.Typ[types.Int], false),
	}, nil), nil)
	r := &Runner{
		bashPPGoSource: true,
		bashPPImports: map[string]string{
			"unsafeheader": "internal/unsafeheader",
			"types":        "cmd/compile/internal/types",
			"unsafe":       "unsafe",
		},
		bashPPTools: bashPPToolchain{nativeTypes: map[string]types.Type{
			"internal/unsafeheader.Slice":      header,
			"cmd/compile/internal/types.Slice": other,
		}},
	}
	target := &syntax.BashPPCollectionType{
		Kind:    "slice",
		Element: &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "int32"}},
	}
	source := &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "unsafeheader.Slice"}}
	if view := r.goSourceUnsafeSliceHeaderView(source, target); view == nil {
		t.Fatal("slice header was not recognized when another imported package also exports Slice")
	}
	notHeader := &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "types.Slice"}}
	if view := r.goSourceUnsafeSliceHeaderView(notHeader, target); view != nil {
		t.Fatal("types.Slice is not a slice header and must not be recognized as one")
	}
	// The same package under an import alias, as a linked multi-file package
	// spells it, and under its full import path.
	r.bashPPImports["__gosource_import_0_7"] = "internal/unsafeheader"
	for _, spelling := range []string{"__gosource_import_0_7.Slice", "internal/unsafeheader.Slice"} {
		aliased := &syntax.BashPPNamedType{Name: &syntax.Lit{Value: spelling}}
		if view := r.goSourceUnsafeSliceHeaderView(aliased, target); view == nil {
			t.Fatalf("slice header spelled %q was not recognized", spelling)
		}
	}
	bare := &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "Slice"}}
	if _, ok := r.goSourceUnsafeStructFields(bare); ok {
		t.Fatal("an unqualified name matching two imported packages must stay ambiguous")
	}
}

// A slice-header literal is built by the program and immediately viewed
// through unsafe, so it is interpreter-owned storage even though its type is
// imported. `s := unsafeheader.Slice{…}` reaches the declaration path, which
// asks bashPPNativeExpr before any composite is evaluated.
func TestGoSourceUnsafeSliceHeaderLiteralIsNotDependencyOwned(t *testing.T) {
	pkg := types.NewPackage("internal/unsafeheader", "unsafeheader")
	header := types.NewNamed(types.NewTypeName(token.NoPos, pkg, "Slice", nil), types.NewStruct([]*types.Var{
		types.NewField(token.NoPos, pkg, "Data", types.Typ[types.UnsafePointer], false),
		types.NewField(token.NoPos, pkg, "Len", types.Typ[types.Int], false),
		types.NewField(token.NoPos, pkg, "Cap", types.Typ[types.Int], false),
	}, nil), nil)
	other := types.NewNamed(types.NewTypeName(token.NoPos, pkg, "String", nil), types.NewStruct([]*types.Var{
		types.NewField(token.NoPos, pkg, "Data", types.Typ[types.UnsafePointer], false),
		types.NewField(token.NoPos, pkg, "Len", types.Typ[types.Int], false),
	}, nil), nil)
	r := &Runner{
		bashPPGoSource: true,
		bashPPImports:  map[string]string{"unsafeheader": "internal/unsafeheader", "unsafe": "unsafe"},
		bashPPTools: bashPPToolchain{nativeTypes: map[string]types.Type{
			"internal/unsafeheader.Slice":  header,
			"internal/unsafeheader.String": other,
		}},
	}
	notHeader := &syntax.BashPPCompositeLit{LitType: &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "unsafeheader.String"}}}
	if !r.bashPPNativeExpr(notHeader) {
		t.Fatal("an ordinary imported struct literal must stay dependency-owned")
	}
	lit := &syntax.BashPPCompositeLit{LitType: &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "unsafeheader.Slice"}}}
	if r.bashPPNativeExpr(lit) {
		t.Fatal("a slice-header literal was classified as dependency-owned")
	}
}
