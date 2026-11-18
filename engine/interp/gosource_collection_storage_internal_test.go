package interp

import (
	"go/constant"
	"testing"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Sprint: #243; Story: #671; Story-ID: 56d156f9118e
func TestGoSourceCollectionStorageRetainsNonJSONValues(t *testing.T) {
	typ := &syntax.BashPPCollectionType{Kind: "slice", Element: &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "complex128"}}}
	meta := &bashPPCollectionMeta{kind: "slice", typ: typ, sequence: make([]*bashPPCollectionMeta, 1, 4)}
	values := make([]any, 1, 4)
	values[0] = complex(2, 3)
	cell := &bashPPCell{declType: typ}

	bashPPStoreCellValue(cell, values, meta)
	stored, ok := cell.vr.Obj.([]any)
	if !ok {
		t.Fatalf("typed collection payload degraded to %T", cell.vr.Obj)
	}
	if got := stored[0]; got != complex(2, 3) {
		t.Fatalf("complex element = %#v", got)
	}
	if cap(stored) != 4 || cap(cell.valueMeta.sequence) != 4 {
		t.Fatalf("capacity lost: payload=%d metadata=%d", cap(stored), cap(cell.valueMeta.sequence))
	}
}

func TestGoSourceCollectionStorageStillValidatesShellObjects(t *testing.T) {
	public := expand.NewObject([]any{complex(2, 3)})
	if _, ok := public.Obj.([]any); ok {
		t.Fatal("shell-facing object unexpectedly admitted a complex value")
	}
	internal := bashPPCollectionVariable([]any{constant.MakeInt64(7)})
	if _, ok := internal.Obj.([]any); !ok {
		t.Fatalf("trusted collection payload degraded to %T", internal.Obj)
	}
}
