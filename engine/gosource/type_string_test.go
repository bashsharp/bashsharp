package gosource

// Sprint: #281; Story: #811; Story-ID: aa5c046bb543

import (
	"go/token"
	"go/types"
	"testing"
)

func mappedTypeStringConverter(prefix string) *converter {
	first := types.NewPackage("example.test/first", "first")
	alpha := types.NewTypeName(token.NoPos, first, "Alpha_9", nil)
	types.NewNamed(alpha, types.NewStruct(nil, nil), nil)
	first.Scope().Insert(alpha)
	unicodeName := types.NewTypeName(token.NoPos, first, "Δ9_名", nil)
	types.NewNamed(unicodeName, types.NewStruct(nil, nil), nil)
	first.Scope().Insert(unicodeName)

	second := types.NewPackage("example.test/second", "second")
	inner := types.NewTypeName(token.NoPos, second, "Inner", nil)
	types.NewNamed(inner, types.NewStruct(nil, nil), nil)
	second.Scope().Insert(inner)

	return &converter{
		prefix:       prefix,
		mapped:       map[string]int{first.Path(): 0, second.Path(): 1},
		mappedPkgs:   []*types.Package{first, second},
		renames:      map[types.Object]string{alpha: "flatAlpha", unicodeName: "flatUnicode", inner: "flatInner"},
		checkerNames: map[string]string{},
	}
}

func TestMappedTypeStringKeepsQualificationSemantics(t *testing.T) {
	c := mappedTypeStringConverter("__consumer_")
	unicodeName := c.mappedPkgs[0].Scope().Lookup("Δ9_名").Type()
	inner := c.mappedPkgs[1].Scope().Lookup("Inner").Type()

	if got, want := c.typeString(types.NewPointer(unicodeName)), "*flatUnicode"; got != want {
		t.Fatalf("Unicode type string = %q, want %q", got, want)
	}
	if got, want := c.typeString(types.NewMap(types.Typ[types.String], types.NewSlice(inner))), "map[string][]flatInner"; got != want {
		t.Fatalf("nested type string = %q, want %q", got, want)
	}
	pattern := c.mappedTypePattern
	if pattern == nil || c.mappedTypeNamePattern() != pattern {
		t.Fatal("typeString did not retain its compiled mapped type-name regexp")
	}
}

func TestMappedTypeStringPatternIsReused(t *testing.T) {
	c := mappedTypeStringConverter("__prefix.[x]+_")
	pattern := c.mappedTypeNamePattern()
	if again := c.mappedTypeNamePattern(); again != pattern {
		t.Fatal("mapped type-name regexp was compiled more than once")
	}

	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "unicode and nested type forms",
			in:   "struct{ A *__prefix.[x]+_pkg_0.Δ9_名; B map[string][]__prefix.[x]+_pkg_1.Inner }",
			want: "struct{ A *flatUnicode; B map[string][]flatInner }",
		},
		{
			name: "different conversion prefix",
			in:   "__other_pkg_0.Alpha_9 | __prefix.[x]+_pkg_0.Alpha_9",
			want: "__other_pkg_0.Alpha_9 | flatAlpha",
		},
		{
			name: "malformed markers",
			in:   "__prefix.[x]+_pkg_.Alpha_9 __prefix.[x]+_pkg_x.Alpha_9 __prefix.[x]+_pkg_0Alpha_9 __prefix.[x]+_pkg_0.",
			want: "__prefix.[x]+_pkg_.Alpha_9 __prefix.[x]+_pkg_x.Alpha_9 __prefix.[x]+_pkg_0Alpha_9 __prefix.[x]+_pkg_0.",
		},
		{
			name: "out of range package index",
			in:   "__prefix.[x]+_pkg_9.Alpha_9",
			want: "__prefix.[x]+_pkg_9.Alpha_9",
		},
		{
			name: "newline does not complete a marker",
			in:   "__prefix.[x]+_pkg_0.\nAlpha_9",
			want: "__prefix.[x]+_pkg_0.\nAlpha_9",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := c.rewriteMappedTypeNames(test.in); got != test.want {
				t.Fatalf("rewrite = %q, want %q", got, test.want)
			}
		})
	}
}

var mappedTypeStringBenchmarkSink string

func BenchmarkMappedTypeStringPatternReuse(b *testing.B) {
	c := mappedTypeStringConverter("__benchmark_")
	typ := types.NewStruct([]*types.Var{
		types.NewField(token.NoPos, nil, "Unicode", c.mappedPkgs[0].Scope().Lookup("Δ9_名").Type(), false),
		types.NewField(token.NoPos, nil, "Nested", types.NewMap(types.Typ[types.String], types.NewSlice(c.mappedPkgs[1].Scope().Lookup("Inner").Type())), false),
	}, nil)
	b.ReportAllocs()
	for range b.N {
		mappedTypeStringBenchmarkSink = c.typeString(typ)
	}
}
