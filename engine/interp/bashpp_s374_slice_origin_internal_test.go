package interp

// Sprint: #374; Story: #1498; Story-ID: cf89c5e06bd1
//
// Transport slice origins are found by backing-storage identity through an
// index, not by scanning every slice view the session ever registered. The
// identity semantics are exactly the former linear scan's: a view wholly
// contained in an already registered live region reuses its tightest
// containing region (smallest capacity, then lowest id, which map iteration
// order must never disturb), and a later wider view receives a new id.

import (
	"testing"
)

func s374SliceViews() (wide, narrow, shifted []any) {
	backing := make([]any, 32)
	for i := range backing {
		backing[i] = i
	}
	wide = backing[:32:32]
	narrow = backing[4:12:12]
	shifted = backing[4:20:20]
	return wide, narrow, shifted
}

// A view wholly contained in a registered region reuses it and reports its
// element offset; disjoint views register fresh identities in order.
func TestS374TransportSliceOriginContainment(t *testing.T) {
	session := &bashPPNativeSession{id: "s"}
	wide, narrow, shifted := s374SliceViews()
	wideID, wideOff := bashPPTransportSliceOrigin(session, wide, nil, nil)
	if wideID == 0 || wideOff != 0 {
		t.Fatalf("wide registration = (%d, %d), want nonzero id and offset 0", wideID, wideOff)
	}
	narrowID, narrowOff := bashPPTransportSliceOrigin(session, narrow, nil, nil)
	if narrowID != wideID {
		t.Fatalf("contained view origin %d, want containing region %d", narrowID, wideID)
	}
	if narrowOff != 4 {
		t.Fatalf("contained view offset %d, want 4", narrowOff)
	}
	shiftedID, shiftedOff := bashPPTransportSliceOrigin(session, shifted, nil, nil)
	if shiftedID != wideID || shiftedOff != 4 {
		t.Fatalf("shifted view = (%d, %d), want (%d, 4)", shiftedID, shiftedOff, wideID)
	}
	other := make([]any, 8)
	otherID, otherOff := bashPPTransportSliceOrigin(session, other, nil, nil)
	if otherID == wideID || otherID == 0 || otherOff != 0 {
		t.Fatalf("disjoint view = (%d, %d), want fresh nonzero id", otherID, otherOff)
	}
	if len(session.sliceOriginKeep) != 2 {
		t.Fatalf("registered regions = %d, want 2 (wide and disjoint)", len(session.sliceOriginKeep))
	}
}

// Choosing the tightest match matters once a later wider view is registered:
// re-transporting the narrow view must resolve to the narrow region, not the
// wider one, and the wider view itself keeps its own new id.
func TestS374TransportSliceOriginTightestAfterWider(t *testing.T) {
	session := &bashPPNativeSession{id: "s"}
	backing := make([]any, 32)
	narrow := backing[4:12:12]
	narrowID, _ := bashPPTransportSliceOrigin(session, narrow, nil, nil)
	wider := backing[:32:32]
	widerID, _ := bashPPTransportSliceOrigin(session, wider, nil, nil)
	if widerID == narrowID {
		t.Fatalf("later wider view reused narrow origin %d, want a new id", narrowID)
	}
	againID, againOff := bashPPTransportSliceOrigin(session, backing[4:12:12], nil, nil)
	if againID != narrowID || againOff != 0 {
		t.Fatalf("narrow re-transport = (%d, %d), want (%d, 0)", againID, againOff, narrowID)
	}
	shiftedID, shiftedOff := bashPPTransportSliceOrigin(session, backing[6:10:10], nil, nil)
	if shiftedID != narrowID || shiftedOff != 2 {
		t.Fatalf("view inside narrow = (%d, %d), want (%d, 2)", shiftedID, shiftedOff, narrowID)
	}
}

// Equal-capacity containers tie-break to the lowest id, exactly as the
// former scan's (capacity, id) minimum did regardless of map order.
func TestS374TransportSliceOriginTieBreaksLowestID(t *testing.T) {
	session := &bashPPNativeSession{id: "s"}
	backing := make([]any, 24)
	first := backing[:10:10]
	second := backing[2:12:12]
	firstID, _ := bashPPTransportSliceOrigin(session, first, nil, nil)
	secondID, _ := bashPPTransportSliceOrigin(session, second, nil, nil)
	if firstID == secondID {
		t.Fatalf("overlapping distinct-capacity views share origin %d", firstID)
	}
	query := backing[3:6:6]
	queryID, queryOff := bashPPTransportSliceOrigin(session, query, nil, nil)
	if queryID != firstID || queryOff != 3 {
		t.Fatalf("tied query = (%d, %d), want (%d, 3)", queryID, queryOff, firstID)
	}
}

// Degenerate views carry no backing identity, before and after indexing.
func TestS374TransportSliceOriginDegenerate(t *testing.T) {
	session := &bashPPNativeSession{id: "s"}
	if id, off := bashPPTransportSliceOrigin(nil, make([]any, 4), nil, nil); id != 0 || off != 0 {
		t.Fatalf("nil session = (%d, %d), want (0, 0)", id, off)
	}
	if id, off := bashPPTransportSliceOrigin(session, nil, nil, nil); id != 0 || off != 0 {
		t.Fatalf("nil view = (%d, %d), want (0, 0)", id, off)
	}
	if id, off := bashPPTransportSliceOrigin(session, make([]any, 0), nil, nil); id != 0 || off != 0 {
		t.Fatalf("empty view = (%d, %d), want (0, 0)", id, off)
	}
	if len(session.sliceOriginKeep) != 0 {
		t.Fatalf("degenerate views registered %d regions", len(session.sliceOriginKeep))
	}
}

// Cost lock: resolving a slice origin must not grow with the number of views
// the session has registered before (the former scan was O(n) with a reflect
// call per entry, quadratic over a long-running program).
func TestS374TransportSliceOriginLookupIsNotLinear(t *testing.T) {
	const registered = 4000
	session := &bashPPNativeSession{id: "s"}
	keep := make([][]any, 0, registered)
	for i := 0; i < registered; i++ {
		view := make([]any, 8)
		keep = append(keep, view)
		bashPPTransportSliceOrigin(session, view, nil, nil)
	}
	if len(session.sliceOriginKeep) != registered {
		t.Fatalf("registered regions = %d, want %d", len(session.sliceOriginKeep), registered)
	}
	probe := make([]any, 8)
	first, off := bashPPTransportSliceOrigin(session, probe, nil, nil)
	if off != 0 {
		t.Fatalf("fresh probe offset %d, want 0", off)
	}
	res := testing.Benchmark(func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if id, _ := bashPPTransportSliceOrigin(session, probe, nil, nil); id != first {
				b.Fatal("origin changed")
			}
		}
	})
	// A 4,000-entry scan costs hundreds of microseconds; an indexed lookup
	// is well under fifty.
	if per := res.NsPerOp(); per > 50000 {
		t.Fatalf("slice origin lookup %d ns/op over %d registered views", per, registered)
	}
}

func BenchmarkS374TransportSliceOrigin(b *testing.B) {
	const registered = 5000
	session := &bashPPNativeSession{id: "s"}
	keep := make([][]any, 0, registered)
	for i := 0; i < registered; i++ {
		view := make([]any, 8)
		keep = append(keep, view)
		bashPPTransportSliceOrigin(session, view, nil, nil)
	}
	probe := make([]any, 8)
	first, _ := bashPPTransportSliceOrigin(session, probe, nil, nil)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if id, _ := bashPPTransportSliceOrigin(session, probe, nil, nil); id != first {
			b.Fatal("origin changed")
		}
	}
}
