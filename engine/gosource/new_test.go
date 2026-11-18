package gosource_test

// Sprint: #118; Story: #57; Story-ID: aae8c426366b
import (
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func shortDecl(t *testing.T, source string) *syntax.BashPPShortDecl {
	t.Helper()
	result, err := gosource.Parse(strings.NewReader(source), "unchanged.go", gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	var decl *syntax.BashPPShortDecl
	syntax.Walk(result.File, func(n syntax.Node) bool {
		if n, ok := n.(*syntax.BashPPShortDecl); ok && decl == nil {
			decl = n
		}
		return true
	})
	if decl == nil {
		t.Fatal("no short declaration converted")
	}
	return decl
}

// new(T) keeps the allocation node so `v := new(Vertex)` reaches the runtime as
// a value expression rather than a generic builtin call.
func TestShortDeclNewTypeKeepsAllocation(t *testing.T) {
	for _, source := range []string{
		"package main\nimport \"fmt\"\nfunc main(){p:=new(int);fmt.Println(*p)}",
		"package main\nimport \"fmt\"\ntype Vertex struct{ X, Y int }\nfunc main(){v:=new(Vertex);fmt.Println(v.X)}",
		"package main\nimport \"fmt\"\nfunc main(){m:=new(map[string]int);fmt.Println(len(*m))}",
	} {
		decl := shortDecl(t, source)
		alloc, ok := decl.Expr.(*syntax.BashPPNewExpr)
		if !ok {
			t.Fatalf("%s: right-hand side %T", source, decl.Expr)
		}
		if alloc.AllocType == nil {
			t.Fatalf("%s: lost allocated type", source)
		}
		if decl.Rhs != nil || decl.Call != nil {
			t.Fatalf("%s: allocation left a shadow right-hand side", source)
		}
	}
}

// Go 1.27 new(v) allocates a copy of a value. The typed node retains both the
// initializer and its defaulted element type, so interpreted and lowered modes
// share the same value semantics.
func TestShortDeclNewValueKeepsInitializer(t *testing.T) {
	decl := shortDecl(t, "package main\nimport \"fmt\"\nfunc main(){p:=new(42);fmt.Println(*p)}")
	alloc, ok := decl.Expr.(*syntax.BashPPNewExpr)
	if !ok {
		t.Fatalf("value allocation converted as %T", decl.Expr)
	}
	if alloc.Init == nil || alloc.AllocType == nil {
		t.Fatal("value allocation lost its initializer or type")
	}
	if decl.Call != nil || decl.Rhs != nil {
		t.Fatal("value allocation left a shadow call or word")
	}
}

// The same guard applies wherever new appears as an expression; a value operand
// must not be converted as a type there either.
func TestNewValueExpressionConverts(t *testing.T) {
	result, err := gosource.Parse(strings.NewReader("package main\nimport \"fmt\"\nfunc main(){fmt.Println(*new(42))}"), "unchanged.go", gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	var alloc *syntax.BashPPNewExpr
	syntax.Walk(result.File, func(n syntax.Node) bool {
		if n, ok := n.(*syntax.BashPPNewExpr); ok {
			alloc = n
		}
		return true
	})
	if alloc == nil || alloc.Init == nil || alloc.AllocType == nil {
		t.Fatal("value operand lost its typed allocation")
	}
}
