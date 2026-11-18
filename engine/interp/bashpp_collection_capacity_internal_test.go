// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

// Sprint: #118; Story: #52; Story-ID: d564bada90bb

import (
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// TestBashPPConvertedStringStorageCapacity checks the storage a string
// conversion allocates against the real allocation the same conversion makes in
// this process, over every length up to a few size classes. The oracle is the
// runtime itself rather than a table, so the answer stays exact if Go's size
// classes change.
//
// The conversions below are written so their results escape, because a result
// the compiler proves non-escaping is given a stack backing store whose
// capacity the heap allocator would never produce; that difference is the
// interpreter's remaining capacity gap and is recorded in
// docs/plan-gosource-collection-capacity.md, not asserted here.
func TestBashPPConvertedStringStorageCapacity(t *testing.T) {
	var sink any
	r, err := New(Lang(syntax.LangBashPP))
	if err != nil {
		t.Fatal(err)
	}
	byteElem := &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "byte"}}
	runeElem := &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "rune"}}
	for n := 0; n <= 129; n++ {
		text := strings.Repeat("a", n)
		bytes := []byte(text)
		sink = bytes
		seq, metas := r.bashPPConvertedStringStorage(byteElem, n, bashPPStringConversionCapacity(text, "byte", false))
		if len(seq) != n || cap(seq) != cap(bytes) {
			t.Errorf("[]byte(%d): storage len %d cap %d, want len %d cap %d", n, len(seq), cap(seq), n, cap(bytes))
		}
		if cap(metas) != cap(seq) {
			t.Errorf("[]byte(%d): metadata cap %d, want %d", n, cap(metas), cap(seq))
		}
		runes := []rune(text)
		sink = runes
		seq, metas = r.bashPPConvertedStringStorage(runeElem, n, bashPPStringConversionCapacity(text, "rune", false))
		if len(seq) != n || cap(seq) != cap(runes) {
			t.Errorf("[]rune(%d): storage len %d cap %d, want len %d cap %d", n, len(seq), cap(seq), n, cap(runes))
		}
		if cap(metas) != cap(seq) {
			t.Errorf("[]rune(%d): metadata cap %d, want %d", n, cap(metas), cap(seq))
		}
	}
	_ = sink
}

// TestBashPPConvertedStringStorageZeroesSpare checks the surplus the allocator
// reserved holds the element's typed zero, not an untyped nil: Go's backing
// arrays are zeroed, so re-slicing a converted slice to its capacity must
// expose zero bytes rather than a value the dependency bridge cannot type.
func TestBashPPConvertedStringStorageZeroesSpare(t *testing.T) {
	r, err := New(Lang(syntax.LangBashPP))
	if err != nil {
		t.Fatal(err)
	}
	elem := &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "byte"}}
	seq, _ := r.bashPPConvertedStringStorage(elem, 3, bashPPStringConversionCapacity("hey", "byte", false))
	if cap(seq) <= 3 {
		t.Fatalf("no spare capacity to check: cap %d", cap(seq))
	}
	for i, value := range seq[:cap(seq)][3:] {
		if value != 0 {
			t.Errorf("spare element %d is %#v, want the byte zero value", i, value)
		}
	}
}
