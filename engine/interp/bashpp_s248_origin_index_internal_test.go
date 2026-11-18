package interp

// Sprint: #248; Story: #702; Story-ID: f330582c10c8
//
// Transport origins are found by storage identity through an index, not by
// scanning every pointer the session ever registered. The identity is exact:
// the same root cell and the same step path (nil and empty paths distinct,
// field names never confused with the step separators).

import (
	"fmt"
	"testing"
)

func TestS248TransportOriginIndexedIdentity(t *testing.T) {
	session := &bashPPNativeSession{id: "s"}
	a, b := &bashPPCell{}, &bashPPCell{}
	cases := []*bashPPPointer{
		{target: a},
		{target: a, path: []bashPPPointerStep{}},
		{target: a, path: []bashPPPointerStep{{field: "x"}}},
		{target: a, path: []bashPPPointerStep{{field: "x", deref: true}}},
		{target: a, path: []bashPPPointerStep{{index: 1}}},
		{target: a, path: []bashPPPointerStep{{index: 1}, {index: 2}}},
		{target: a, path: []bashPPPointerStep{{index: 12}}},
		{target: a, path: []bashPPPointerStep{{field: "1:x,0;"}}},
		{target: a, path: []bashPPPointerStep{{field: "1"}, {field: "x"}}},
		{target: b},
		{target: b, path: []bashPPPointerStep{{field: "x"}}},
	}
	ids := map[uint64]int{}
	for i, ptr := range cases {
		id := bashPPTransportOrigin(session, ptr)
		if prev, dup := ids[id]; dup {
			t.Fatalf("case %d shares origin %d with case %d", i, id, prev)
		}
		ids[id] = i
		if want := uint64(i + 1); id != want {
			t.Fatalf("case %d: origin %d, want %d (allocation order)", i, id, want)
		}
	}
	// The same storage spelled by a fresh pointer value resolves to the
	// origin registered first; nothing new is registered.
	for i, ptr := range cases {
		again := &bashPPPointer{target: ptr.target, elem: ptr.elem}
		if ptr.path != nil {
			again.path = append([]bashPPPointerStep{}, ptr.path...)
		}
		if id := bashPPTransportOrigin(session, again); id != uint64(i+1) {
			t.Fatalf("case %d again: origin %d, want %d", i, id, i+1)
		}
	}
	if len(session.origins) != len(cases) || session.originNext != uint64(len(cases)) {
		t.Fatalf("re-resolution registered new origins: %d/%d", len(session.origins), session.originNext)
	}
	if len(session.originIndex) != len(cases) {
		t.Fatalf("index covers %d identities, want %d", len(session.originIndex), len(cases))
	}
}

// A table populated outside bashPPTransportOrigin (a session restored by a
// test, or entries added by another path) is reindexed before use, so a
// lookup never misses a registered identity.
func TestS248TransportOriginReindexesForeignEntries(t *testing.T) {
	cell := &bashPPCell{}
	session := &bashPPNativeSession{id: "s", origins: map[uint64]*bashPPOriginRecord{
		7: newOriginRecord(&bashPPPointer{target: cell, path: []bashPPPointerStep{{field: "f"}}}),
	}, originNext: 7}
	if id := bashPPTransportOrigin(session, &bashPPPointer{target: cell, path: []bashPPPointerStep{{field: "f"}}}); id != 7 {
		t.Fatalf("foreign entry origin %d, want 7", id)
	}
	if id := bashPPTransportOrigin(session, &bashPPPointer{target: cell}); id != 8 {
		t.Fatalf("new identity origin %d, want 8", id)
	}
}

// Cost lock: resolving an origin must not grow with the number of pointers
// the session has registered before (the former scan was O(n) with a
// reflect.DeepEqual per entry, quadratic over a long-running program).
func TestS248TransportOriginLookupIsNotLinear(t *testing.T) {
	session := &bashPPNativeSession{id: "s"}
	for i := 0; i < 20000; i++ {
		bashPPTransportOrigin(session, &bashPPPointer{target: &bashPPCell{}, path: []bashPPPointerStep{{field: fmt.Sprint(i)}}})
	}
	probe := &bashPPPointer{target: &bashPPCell{}}
	first := bashPPTransportOrigin(session, probe)
	res := testing.Benchmark(func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if bashPPTransportOrigin(session, probe) != first {
				b.Fatal("origin changed")
			}
		}
	})
	// A 20,000-entry scan costs hundreds of microseconds; an indexed lookup
	// is well under ten.
	if per := res.NsPerOp(); per > 10000 {
		t.Fatalf("origin lookup %d ns/op over 20000 registered origins", per)
	}
}

func BenchmarkS248TransportOrigin(b *testing.B) {
	session := &bashPPNativeSession{id: "s"}
	for i := 0; i < 5000; i++ {
		bashPPTransportOrigin(session, &bashPPPointer{target: &bashPPCell{}})
	}
	probe := &bashPPPointer{target: &bashPPCell{}, path: []bashPPPointerStep{{field: "x"}, {index: 3}}}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bashPPTransportOrigin(session, probe)
	}
}
