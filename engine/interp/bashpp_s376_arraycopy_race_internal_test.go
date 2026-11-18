// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

// Sprint: #376; Story: #1551; Story-ID: b22ffea69b27

import (
	"sync"
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

func s376ScalarIntArrayType() *syntax.BashPPCollectionType {
	return &syntax.BashPPCollectionType{
		Kind:    "array",
		Element: &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "int"}},
	}
}

// TestS376ArrayCopyConcurrentReadOnly ensures concurrent by-value copies of the
// same source array never race. Copying an array by value is a read-only
// operation on the source in Go, so the read path must not lazily materialize
// the hasValueElements cache on the shared source meta. Both metas below
// deliberately carry no precomputed flags. Run with -race.
func TestS376ArrayCopyConcurrentReadOnly(t *testing.T) {
	scalarTyp := s376ScalarIntArrayType()
	scalarValue := []any{1, 2, 3, 4}
	scalarMeta := &bashPPCollectionMeta{
		kind:     "array",
		typ:      scalarTyp,
		sequence: make([]*bashPPCollectionMeta, len(scalarValue)),
		// No precomputed flags: exercises the lazy read path concurrently.
	}

	innerTyp := s376ScalarIntArrayType()
	nestedTyp := &syntax.BashPPCollectionType{Kind: "array", Element: innerTyp}
	nestedValue := []any{[]any{1, 2}, []any{3, 4}}
	newNestedChild := func() *bashPPCollectionMeta {
		return &bashPPCollectionMeta{
			kind:     "array",
			typ:      innerTyp,
			sequence: make([]*bashPPCollectionMeta, 2),
		}
	}
	nestedMeta := &bashPPCollectionMeta{
		kind:     "array",
		typ:      nestedTyp,
		sequence: []*bashPPCollectionMeta{newNestedChild(), newNestedChild()},
	}

	const workers = 16
	const copiesPerWorker = 25

	runCase := func(name string, value []any, meta *bashPPCollectionMeta, check func(t *testing.T, out any, copyMeta *bashPPCollectionMeta)) {
		t.Helper()
		var wg sync.WaitGroup
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < copiesPerWorker; i++ {
					out, gotMeta := bashPPCopyArrayValue(value, meta)
					func() {
						// Spot-check each copy under the test mutex via Errorf only on failure.
						outSeq, ok := out.([]any)
						if !ok {
							t.Errorf("%s: copy %d: expected []any, got %T", name, i, out)
							return
						}
						if len(outSeq) != len(value) {
							t.Errorf("%s: copy %d: length = %d, want %d", name, i, len(outSeq), len(value))
							return
						}
						if gotMeta == nil || !gotMeta.hasValueElementsKnown {
							t.Errorf("%s: copy %d: copy must carry computed flags forward", name, i)
							return
						}
						check(t, out, gotMeta)
					}()
				}
			}()
		}
		wg.Wait()
		if meta.hasValueElementsKnown {
			t.Errorf("%s: source meta was mutated on the read path (hasValueElementsKnown=true)", name)
		}
	}

	t.Run("scalar-bulk", func(t *testing.T) {
		runCase("scalar", scalarValue, scalarMeta, func(t *testing.T, out any, _ *bashPPCollectionMeta) {
			t.Helper()
			outSeq := out.([]any)
			for i, v := range scalarValue {
				if outSeq[i] != v {
					t.Errorf("scalar: out[%d] = %v, want %v", i, outSeq[i], v)
					break
				}
			}
		})
		// Copies are independent values: mutating one must not touch the source.
		out, _ := bashPPCopyArrayValue(scalarValue, scalarMeta)
		out.([]any)[0] = 99
		if scalarValue[0] != 1 {
			t.Errorf("scalar: source payload mutated via copy: scalarValue[0] = %v", scalarValue[0])
		}
	})

	t.Run("nested-recursive", func(t *testing.T) {
		runCase("nested", nestedValue, nestedMeta, func(t *testing.T, out any, _ *bashPPCollectionMeta) {
			t.Helper()
			outSeq := out.([]any)
			for i, inner := range nestedValue {
				gotInner, ok := outSeq[i].([]any)
				if !ok {
					t.Errorf("nested: out[%d] is %T, want []any", i, outSeq[i])
					return
				}
				wantInner := inner.([]any)
				for j, v := range wantInner {
					if gotInner[j] != v {
						t.Errorf("nested: out[%d][%d] = %v, want %v", i, j, gotInner[j], v)
						break
					}
				}
			}
		})
		// Recursive value semantics: mutating a nested copy must not touch the source.
		out, _ := bashPPCopyArrayValue(nestedValue, nestedMeta)
		out.([]any)[0].([]any)[0] = 99
		if nestedValue[0].([]any)[0] != 1 {
			t.Errorf("nested: source payload mutated via copy")
		}
		for _, child := range nestedMeta.sequence {
			if child.hasValueElementsKnown {
				t.Errorf("nested: shared child meta was mutated on the read path")
				break
			}
		}
	})
}
