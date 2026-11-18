package interp

import (
	"testing"
	"unsafe"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Exercise comparison without a Go-source runner: Bash# must enumerate scalar
// fields even when they carry no runtime layout, including inside arrays.
func TestS376SparseStructComparisonAndCopy(t *testing.T) {
	typ := &syntax.BashPPStructType{Fields: []*syntax.BashPPField{{
		Names:         []*syntax.Lit{{Value: "A"}, {Value: "B"}},
		FieldTypeExpr: &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "int"}},
	}}}
	r := &Runner{}
	value, meta := r.bashPPZeroValue(typ)
	copyValue, copyMeta := bashPPCopyArrayValue(value, meta)
	check := func(want bool) {
		t.Helper()
		got, err := bashPPCompareValues(value, meta, false, copyValue, copyMeta, false)
		if err != nil || got != want {
			t.Fatalf("struct comparison = %v, %v; want %v", got, err, want)
		}
	}
	check(true)
	bashPPStorageSetField(copyValue.(map[string]any), copyMeta.mapping, "A", "9", nil)
	check(false)
	bashPPStorageSetField(value.(map[string]any), meta.mapping, "A", "9", nil)
	check(true)
	arrayType := &syntax.BashPPCollectionType{Kind: "array", Length: &syntax.Lit{Value: "1"}, Element: typ}
	arrayMeta := &bashPPCollectionMeta{kind: "array", typ: arrayType, sequence: []*bashPPCollectionMeta{meta}}
	array := []any{value}
	other, otherMeta := bashPPCopyArrayValue(array, arrayMeta)
	nested := other.([]any)[0].(map[string]any)
	bashPPStorageSetField(nested, otherMeta.sequence[0].mapping, "B", "4", nil)
	if equal, err := bashPPCompareValues(array, arrayMeta, false, other, otherMeta, false); err != nil || equal {
		t.Fatalf("nested scalar mismatch lost: equal=%v err=%v", equal, err)
	}
}

func TestS376StructInitializerClearsLayout(t *testing.T) {
	value := make(map[string]any)
	meta := &bashPPCollectionMeta{kind: "struct"}
	child := &bashPPCollectionMeta{kind: "func"}
	bashPPSetInitialStructField(value, meta, "F", nil, child)
	bashPPSetInitialStructField(value, meta, "F", "closure", nil)
	if got := bashPPLayoutGet(meta.mapping, "F"); got != nil {
		t.Fatal("initializer retained zero-value metadata for the replacement")
	}
	if got, ok := bashPPStorageGet(value, "F"); !ok || got != "closure" {
		t.Fatal("field disappeared with its layout")
	}
}

func BenchmarkS376ScalarStructStorage(b *testing.B) {
	typ := &syntax.BashPPStructType{Fields: []*syntax.BashPPField{{
		Names:         []*syntax.Lit{{Value: "A"}, {Value: "B"}, {Value: "C"}, {Value: "D"}},
		FieldTypeExpr: &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "int"}},
	}}}
	r := &Runner{}
	b.ReportAllocs()
	for b.Loop() {
		value, meta := r.bashPPZeroValue(typ)
		bashPPCopyArrayValue(value, meta)
	}
}

// Zero creation plus value-copy should not allocate two scalar-only layout
// maps. This small allocation budget regresses before sparse layout storage.
func TestS376ScalarStructAllocationBudget(t *testing.T) {
	typ := &syntax.BashPPStructType{Fields: []*syntax.BashPPField{{
		Names:         []*syntax.Lit{{Value: "A"}, {Value: "B"}, {Value: "C"}, {Value: "D"}},
		FieldTypeExpr: &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "int"}},
	}}}
	r := &Runner{}
	allocs := testing.AllocsPerRun(100, func() {
		value, meta := r.bashPPZeroValue(typ)
		bashPPCopyArrayValue(value, meta)
	})
	if allocs > 12 {
		t.Fatalf("scalar struct creation and copy: %.0f allocations, budget 12", allocs)
	}
}

// A retained *T is a pointer, a cell and a payload; the rarely used
// unsafe-conversion state must stay out of line so that a slice of a million
// pointers does not pay for it a million times.
func TestS376PointerAndCellStayCompact(t *testing.T) {
	if got := unsafe.Sizeof(bashPPPointer{}); got > 64 {
		t.Fatalf("bashPPPointer is %d bytes; unsafe-conversion state must live in bashPPPointerCold", got)
	}
	if got := unsafe.Sizeof(bashPPCell{}); got > 256 {
		t.Fatalf("bashPPCell is %d bytes; want at most 256 (the 256-byte allocator class)", got)
	}
	var p bashPPPointer
	if p.unsafeSource() != nil || p.unsafeSlice() != nil || p.unsafeOffset() != 0 {
		t.Fatal("zero pointer reports unsafe state")
	}
	retyped := p
	retyped.setUnsafeOffset(8)
	if p.cold != nil || retyped.unsafeOffset() != 8 {
		t.Fatal("setting unsafe state on a copy wrote through to the original")
	}
	retyped.setUnsafeOffset(0)
	if retyped.cold != nil {
		t.Fatal("clearing the last unsafe field kept the cold record")
	}
}

func TestS376PointerMetaIsSharedPerType(t *testing.T) {
	typ := &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "T"}}
	a, b := bashPPPointerMeta(typ), bashPPPointerMeta(typ)
	if a != b {
		t.Fatal("pointer layout records for one type are not shared")
	}
	other := &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "T"}}
	if bashPPPointerMeta(other) == a {
		t.Fatal("distinct type nodes share a pointer layout record")
	}
}

func TestS376PureShortDeclSkipsBlockSnapshot(t *testing.T) {
	lit := func(s string) *syntax.Lit { return &syntax.Lit{Value: s} }
	ident := &syntax.BashPPIdent{Name: lit("x")}
	decl := func(rhs ...syntax.BashPPExpr) *syntax.BashPPShortDecl {
		return &syntax.BashPPShortDecl{Lhs: []*syntax.Lit{lit("y")}, RhsExprs: rhs}
	}
	if !bashPPPureShortDecl(decl(ident, &syntax.BashPPBasicLit{Value: lit("1"), Kind: "INT"})) {
		t.Fatal("a read of names and literals was not classified as pure")
	}
	if bashPPPureShortDecl(decl(&syntax.BashPPSelectorExpr{X: ident, Sel: lit("M"), MethodValue: true})) {
		t.Fatal("a method value was classified as pure")
	}
	call := decl(ident)
	call.Call = &syntax.BashPPCall{}
	if bashPPPureShortDecl(call) {
		t.Fatal("a call was classified as pure")
	}
	if bashPPPureShortDecl(&syntax.BashPPShortDecl{Lhs: []*syntax.Lit{lit("y")}}) {
		t.Fatal("a declaration without a parsed expression was classified as pure")
	}
}
