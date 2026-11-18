package interp

// Sprint: #209; Story: #461; Story-ID: 4ed649697945
import (
	"go/constant"
	"testing"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestGoSourceSprint209FloatArgumentCarrierDoesNotCoerceString(t *testing.T) {
	r := &Runner{bashPPGoSource: true}
	params := []bashPPParam{{
		name:     "x",
		declared: "float64",
		typ:      &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "float64"}},
	}}
	cells := []*bashPPCell{{
		vr:         expand.Variable{Set: true, Kind: expand.String, Str: "6/5"},
		scalarKind: constant.String,
	}}
	args, gotCells, err := r.goSourceContextualFloatCallArgs(params, []string{"6/5"}, cells, false)
	if err != nil {
		t.Fatal(err)
	}
	if args[0] != "6/5" || gotCells[0] != cells[0] {
		t.Fatalf("string carrier was coerced: args=%q sameCell=%v", args, gotCells[0] == cells[0])
	}
	if r.bashPPValueFits("float64", args[0]) {
		t.Fatalf("string %q unexpectedly accepted as float64", args[0])
	}
}

func TestGoSourceSprint209FloatArgumentCarrierRoundsExactText(t *testing.T) {
	r := &Runner{bashPPGoSource: true}
	params := []bashPPParam{{
		name:     "x",
		declared: "float64",
		typ:      &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "float64"}},
	}}
	cells := []*bashPPCell{{
		vr:         expand.Variable{Set: true, Kind: expand.String, Str: "5404319552844595/4503599627370496"},
		scalarKind: constant.Float,
	}}
	args, gotCells, err := r.goSourceContextualFloatCallArgs(params, []string{cells[0].vr.Str}, cells, false)
	if err != nil {
		t.Fatal(err)
	}
	if args[0] != "1.2" || gotCells[0] == cells[0] || gotCells[0].vr.String() != cells[0].vr.String() {
		t.Fatalf("carrier arg=%q cell=%q sameCell=%v, want rounded arg and exact copied cell", args[0], gotCells[0].vr.String(), gotCells[0] == cells[0])
	}
}
