package interp

import "testing"

// Sprint: #270; Story: #759; Story-ID: 9fa63957d084
func TestGoSourceOpaqueIDsSharedAcrossTaskSnapshots(t *testing.T) {
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	r.Reset()
	left := r.subshell(false)
	right := r.subshell(false)
	first := &bashPPCell{}
	second := &bashPPCell{}
	firstID := left.goSourceUnsafeOpaqueCellID(first)
	if got := right.goSourceUnsafeOpaqueCellID(first); got != firstID {
		t.Fatalf("shared cell IDs differ: %d, %d", firstID, got)
	}
	if got := right.goSourceUnsafeOpaqueCellID(second); got == firstID {
		t.Fatalf("distinct cells share opaque ID %d", got)
	}
}
