package interp

import (
	"github.com/bashsharp/bashsharp/engine/syntax"
	"go/importer"
	"go/types"
	"testing"
)

func TestGoSourceNativeEmbeddedMethodSets(t *testing.T) {
	imp := importer.Default()
	r := &Runner{bashPPGoSource: true, bashPPImports: map[string]string{"sync": "sync", "bytes": "bytes", "bufio": "bufio"}}
	r.bashPPTools.nativeTypes = map[string]types.Type{}
	for _, path := range []string{"sync", "bytes", "bufio"} {
		pkg, err := imp.Import(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range pkg.Scope().Names() {
			if obj, ok := pkg.Scope().Lookup(name).(*types.TypeName); ok {
				r.bashPPTools.nativeTypes[path+"."+name] = obj.Type()
			}
		}
	}
	field := func(name string, pointer bool) *syntax.BashPPField {
		var typ syntax.BashPPTypeExpr = &syntax.BashPPNamedType{Name: &syntax.Lit{Value: name}}
		if pointer {
			typ = &syntax.BashPPPointerType{Element: typ}
		}
		return &syntax.BashPPField{Embedded: true, FieldTypeExpr: typ}
	}
	root := &syntax.BashPPStructType{Fields: []*syntax.BashPPField{field("sync.Mutex", false), field("bytes.Buffer", true)}}
	for _, tc := range []struct {
		name        string
		addressable bool
		want        string
	}{
		{"Lock", true, "Mutex"}, {"Write", true, "Buffer"}, {"Lock", false, ""}, {"Write", false, "Buffer"}, {"Missing", true, ""},
	} {
		edges, ok := r.bashPPResolveNativeEmbedded(root, tc.name, tc.addressable)
		if tc.want == "" {
			if ok {
				t.Fatalf("unexpected %s selection %v", tc.name, edges)
			}
		} else if !ok || len(edges) != 1 || edges[0].name != tc.want {
			t.Fatalf("%s addressable%v: %v %v", tc.name, tc.addressable, edges, ok)
		}
	}
	// An imported type's internally promoted method retains its true depth.
	root.Fields = []*syntax.BashPPField{field("bufio.ReadWriter", true), field("bytes.Buffer", true)}
	edges, ok := r.bashPPResolveNativeEmbedded(root, "ReadString", true)
	if !ok || len(edges) != 1 || edges[0].name != "Buffer" {
		t.Fatalf("native internal depth: %v %v", edges, ok)
	}
	root.Fields = []*syntax.BashPPField{field("sync.Mutex", false), field("sync.RWMutex", false)}
	if edges, ok := r.bashPPResolveNativeEmbedded(root, "Lock", true); ok {
		t.Fatalf("ambiguous methods accepted: %v", edges)
	}
	// An unavailable shallow pointer method still blocks a deeper native
	// method of the same name; it must not silently select that deeper body.
	r.bashPPTypes = map[string]bashPPType{"Outer": {typeExpr: &syntax.BashPPStructType{Fields: []*syntax.BashPPField{field("bytes.Buffer", true)}}}}
	r.bashPPMethods = map[string]map[string]*bashPPFunc{"Outer": {"ReadString": {decl: &syntax.BashPPFuncDecl{Receiver: &syntax.BashPPReceiver{Pointer: true}}}}}
	outer := &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "Outer"}}
	if _, ok := r.bashPPResolveNativeEmbedded(outer, "ReadString", false); ok {
		t.Fatal("inaccessible shallow pointer method bypassed")
	}
	// Display spellings without an accepted import binding cannot grant a set.
	delete(r.bashPPImports, "sync")
	if _, ok := r.bashPPResolveNativeEmbedded(root, "Lock", true); ok {
		t.Fatal("unauthenticated native type spelling accepted")
	}
}

func TestGoSourceImportedInterfaceMethodSetIsNarrow(t *testing.T) {
	imp := importer.Default()
	r := &Runner{
		bashPPGoSource: true,
		bashPPImports:  map[string]string{"fmt": "fmt", "strings": "strings"},
	}
	r.bashPPTools.nativeTypes = map[string]types.Type{}
	for _, path := range []string{"fmt", "strings"} {
		pkg, err := imp.Import(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"Stringer", "Builder"} {
			if obj, ok := pkg.Scope().Lookup(name).(*types.TypeName); ok {
				r.bashPPTools.nativeTypes[path+"."+name] = obj.Type()
			}
		}
	}
	stringer := &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "fmt.Stringer"}}
	if _, ok := r.bashPPInterfaceType(stringer); !ok {
		t.Fatal("authenticated imported interface was treated as concrete")
	}
	concrete := &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "strings.Builder"}}
	if _, ok := r.bashPPInterfaceType(concrete); ok {
		t.Fatal("authenticated imported concrete type was treated as interface")
	}
}
