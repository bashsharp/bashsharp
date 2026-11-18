//go:build full

package interp

import (
	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"math"
	"testing"
)

// Sprint: #219; Story: #460; Story-ID: d8e7d58f362b
func TestS219ArrayMethodRegressionCarrier(t *testing.T) {
	r := &Runner{bashPPGoSource: true}
	typ := &syntax.BashPPCollectionType{Kind: "array", Length: &syntax.Lit{Value: "3"}, Element: &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "float64"}}}
	meta := &bashPPCollectionMeta{kind: "array", typ: typ, sequence: make([]*bashPPCollectionMeta, 3)}
	payload := []any{math.Inf(1), math.NaN(), 4.0}
	vr, ok := r.bashPPGoSourceCollectionCarrier(payload, meta)
	if !ok {
		t.Fatal("Go array carrier rejected")
	}
	source := &bashPPCell{vr: vr, valueMeta: meta, object: &bashPPObjectIdentity{}}
	copied := bashPPCopyAssignmentCell(source)
	values, ok := copied.vr.Obj.([]any)
	if !ok {
		t.Fatalf("copied carrier became %T", copied.vr.Obj)
	}
	if !math.IsInf(values[0].(float64), 1) || !math.IsNaN(values[1].(float64)) {
		t.Fatal("nonfinite array elements lost")
	}
	values[2] = 9.0
	if payload[2] != 4.0 {
		t.Fatal("array copy aliases source")
	}
	if _, admitted := expand.NewObject(payload).Obj.([]any); admitted {
		t.Fatal("ordinary JSON boundary admitted nonfinite array")
	}
}
