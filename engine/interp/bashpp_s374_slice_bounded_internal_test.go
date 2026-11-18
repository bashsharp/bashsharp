package interp

// Sprint: #374; Story: #1523; Story-ID: ae2d1027dda7
//
// Transported slice regions must stay bounded over many round trips.
//
// Every slice that crosses the bridge registers its backing interval in the
// session's keep-live table (and the interval index over it) so overlapping
// views share one backing identity. That table was append-only for the life
// of the session: one million callback round trips with fresh slices — the
// fixedbugs/issue39541 shape, each call transporting a fresh slice into the
// interpreter and back — retained every slice that ever crossed, growing
// without bound until the host killed the process. Each loop iteration below
// registers exactly as one such round trip does, through the same funnel the
// collection transport uses; the table must stay capped no matter how many
// distinct backings cross.

import (
	"testing"
)

// Many fresh slices cross; the region table must stay bounded.
func TestS374TransportSliceOriginStaysBounded(t *testing.T) {
	session := &bashPPNativeSession{id: "s"}
	const trips = 20000
	// Hold every backing alive, as live interpreter values would: each trip
	// then names a distinct backing interval, the shape that grew the table
	// without bound.
	keep := make([][]any, 0, trips)
	for i := 0; i < trips; i++ {
		view := make([]any, 8)
		keep = append(keep, view)
		if id, off := bashPPTransportSliceOrigin(session, view, nil, nil); id == 0 || off != 0 {
			t.Fatalf("trip %d: fresh view = (%d, %d), want nonzero id and offset 0", i, id, off)
		}
	}
	if n := len(session.sliceOriginKeep); n > sliceRegionKeepCap {
		t.Fatalf("transported slice regions = %d after %d trips, want at most %d", n, trips, sliceRegionKeepCap)
	}
	if session.sliceRegionCount != len(session.sliceOriginKeep) {
		t.Fatalf("region index covers %d of %d registered regions", session.sliceRegionCount, len(session.sliceOriginKeep))
	}
	// A live view still crosses after the bound applied: a retained slice
	// stays usable no matter how many transient slices crossed since.
	if id, _ := bashPPTransportSliceOrigin(session, keep[trips-1], nil, nil); id == 0 {
		t.Fatalf("live view lost its backing identity after bounded transport")
	}
}
