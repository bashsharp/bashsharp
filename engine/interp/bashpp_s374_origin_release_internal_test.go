package interp

import (
	"os/exec"
	"runtime"
	"testing"
)

// TestS374OriginReleaseDropsDeadCells proves the interpreter origin table is
// bounded by live storage, not by every storage ever transported. Origins
// whose cells are unreachable from any interpreter value must leave the
// table (and queue a worker release) once swept; live cells must keep their
// identity.
func TestS374OriginReleaseDropsDeadCells(t *testing.T) {
	s := &bashPPNativeSession{id: "origin-release-test"}
	const total = 64
	ids := make([]uint64, 0, total)
	var live []*bashPPCell
	for i := 0; i < total; i++ {
		cell := &bashPPCell{}
		ids = append(ids, bashPPTransportOrigin(s, &bashPPPointer{target: cell}))
		if i%32 == 0 {
			live = append(live, cell)
		}
	}
	if got := len(s.origins); got != total {
		t.Fatalf("origins table has %d entries, want %d", got, total)
	}
	for i := 0; i < 3; i++ {
		runtime.GC()
	}
	released := s.sweepTransportOrigins(true)
	if len(released) != total-len(live) {
		t.Fatalf("sweep released %d origins, want %d", len(released), total-len(live))
	}
	if got := len(s.origins); got != len(live) {
		t.Fatalf("origins table has %d entries after sweep, want %d", got, len(live))
	}
	// Live cells keep their origin identity across the sweep.
	for i, cell := range live {
		want := ids[(i * 32)]
		if got := bashPPTransportOrigin(s, &bashPPPointer{target: cell}); got != want {
			t.Fatalf("live cell %d re-registered as origin %d, want %d", i, got, want)
		}
	}
	// Dead origins are gone and queued exactly once.
	for i, id := range ids {
		if i%32 == 0 {
			continue
		}
		if _, known := s.originLookup(id); known {
			t.Fatalf("dead origin %d still known after sweep", id)
		}
	}
	pending := s.takeOriginReleases(nil)
	if len(pending) != len(released) {
		t.Fatalf("release queue has %d origins, want %d", len(pending), len(released))
	}
	if again := s.sweepTransportOrigins(true); len(again) != 0 {
		t.Fatalf("second sweep released %d origins, want 0", len(again))
	}
	runtime.KeepAlive(live)
}

// TestS374OriginReleaseRoundTripsBounded drives many single-call transports
// with dead cells between sweeps, mirroring sequential corpus rows: the
// table must stay near its cap instead of growing with the trip count.
func TestS374OriginReleaseRoundTripsBounded(t *testing.T) {
	oldCap := bashPPOriginKeepCap
	bashPPOriginKeepCap = 32
	defer func() { bashPPOriginKeepCap = oldCap }()
	s := &bashPPNativeSession{id: "origin-release-trips"}
	const trips = 512
	drained := 0
	peak := 0
	for i := 0; i < trips; i++ {
		cell := &bashPPCell{}
		bashPPTransportOrigin(s, &bashPPPointer{target: cell})
		drained += len(s.takeOriginReleases(nil))
		if n := len(s.origins); n > peak {
			peak = n
		}
	}
	for i := 0; i < 3; i++ {
		runtime.GC()
	}
	s.sweepTransportOrigins(true)
	drained += len(s.takeOriginReleases(nil))
	if got := len(s.origins); got != 0 {
		t.Fatalf("origins table has %d entries after %d dead trips, want 0", got, trips)
	}
	if drained != trips {
		t.Fatalf("drained %d releases over %d trips, want %d", drained, trips, trips)
	}
	if peak > 2*bashPPOriginKeepCap {
		t.Fatalf("origins table peaked at %d entries, want below %d", peak, 2*bashPPOriginKeepCap)
	}
}

// TestS374OriginReleaseUnsafeStaysPinned proves the conservative path:
// pointers carrying unsafe derivations are never proposed for release.
func TestS374OriginReleaseUnsafeStaysPinned(t *testing.T) {
	s := &bashPPNativeSession{id: "origin-release-pinned"}
	cell := &bashPPCell{}
	id := bashPPTransportOrigin(s, &bashPPPointer{target: cell, cold: &bashPPPointerCold{forged: true}})
	for i := 0; i < 3; i++ {
		runtime.GC()
	}
	if released := s.sweepTransportOrigins(true); len(released) != 0 {
		t.Fatalf("sweep released %d origins, want 0 (forged stays pinned)", len(released))
	}
	if _, known := s.originLookup(id); !known {
		t.Fatalf("pinned origin %d lost after sweep", id)
	}
	runtime.KeepAlive(cell)
}

// TestS374OriginReleasePrunesWorker proves the generated worker drops every
// per-origin index for released origins, so its table is bounded by live
// origins rather than by every origin ever decoded.
func TestS374OriginReleasePrunesWorker(t *testing.T) {
	binary := s374OwnershipWorker(t, `
func main() {
 bridgeAuth="0123456789abcdef0123456789abcdef"
 const total = 256
 ids := make([]uint64, 0, total)
 for i:=0;i<total;i++ {
  origin := uint64(i+1)
  v := reflect.New(reflect.TypeFor[int]())
  originalPointers.Lock()
  originalPointers.values[origin]=v
  originalPointers.ids[v.Pointer()]=origin
  typedPointerOrigins[pointerKey{v.Pointer(),v.Type()}]=origin
  if originPointerAddrs[origin]==nil{originPointerAddrs[origin]=map[uintptr]bool{}}
  originPointerAddrs[origin][v.Pointer()]=true
  if typedPointerOriginKeys[origin]==nil{typedPointerOriginKeys[origin]=map[pointerKey]bool{}}
  typedPointerOriginKeys[origin][pointerKey{v.Pointer(),v.Type()}]=true
  originalPointers.sent[origin]="snapshot"
  retainedPointerOrigins[origin]=true
  originalPointers.Unlock()
  originalArrays.Lock();originalArrays.values[origin]=reflect.New(reflect.TypeFor[[4]int]());originalArrays.Unlock()
  ids = append(ids, origin)
 }
 dropped := releaseOrigins(ids)
 for i:=0;i<5;i++ { bppRuntime.GC();time.Sleep(5*time.Millisecond) }
 originalPointers.Lock()
 stats:=[]int{len(originalPointers.values),len(originalPointers.ids),len(originalPointers.sent),len(retainedPointerOrigins),len(typedPointerOrigins),len(originPointerAddrs)}
 originalPointers.Unlock()
 originalArrays.Lock();arrays:=len(originalArrays.values);originalArrays.Unlock()
 fmt.Printf("dropped=%d values=%d ids=%d sent=%d retained=%d typed=%d addrs=%d arrays=%d\n",dropped,stats[0],stats[1],stats[2],stats[3],stats[4],stats[5],arrays)
 if dropped != total { panic(fmt.Sprintf("releaseOrigins dropped %d, want %d", dropped, total)) }
 for _,n := range stats { if n>4 { panic(fmt.Sprintf("worker origin table has %d entries, want <=4", n)) } }
 if arrays>4 { panic(fmt.Sprintf("worker array placements have %d entries, want <=4", arrays)) }
}
`)
	if output, err := exec.Command(binary).CombinedOutput(); err != nil {
		t.Fatalf("worker: %v: %s", err, output)
	} else {
		t.Log(string(output))
	}
}

// TestS374OriginSweepIsAmortisedOverLiveOrigins pins the cost of a table
// that is over the cap but entirely live (test/fixedbugs/issue27695.go with
// two processors): an unforced sweep that found nothing to release must not
// run again, with its full collection, until the table has doubled.
func TestS374OriginSweepIsAmortisedOverLiveOrigins(t *testing.T) {
	old := bashPPOriginKeepCap
	bashPPOriginKeepCap = 8
	defer func() { bashPPOriginKeepCap = old }()
	s := &bashPPNativeSession{id: "origin-sweep-amortised"}
	var live []*bashPPCell
	add := func(n int) {
		for i := 0; i < n; i++ {
			cell := &bashPPCell{}
			live = append(live, cell)
			bashPPTransportOrigin(s, &bashPPPointer{target: cell})
		}
	}
	add(16)
	if next := s.originSweepNext; next < 2*bashPPOriginKeepCap {
		t.Fatalf("no sweep threshold recorded after passing the cap: %d", next)
	}
	threshold := s.originSweepNext
	// Below the threshold an unforced sweep is a no-op: it must leave the
	// identity index in place, which a real sweep drops before collecting.
	s.mu.Lock()
	s.reindexOriginsLocked()
	s.mu.Unlock()
	if released := s.sweepTransportOrigins(false); released != nil {
		t.Fatalf("unforced sweep below the threshold released %d origins", len(released))
	}
	if s.originIndex == nil {
		t.Fatal("unforced sweep below the threshold still ran a collection pass")
	}
	if s.originSweepNext != threshold {
		t.Fatalf("threshold moved without a sweep: %d, want %d", s.originSweepNext, threshold)
	}
	// A forced sweep (an explicit runtime.GC in the program) is never skipped.
	for i := 1; i < len(live); i++ {
		live[i] = nil
	}
	for i := 0; i < 3; i++ {
		runtime.GC()
	}
	if released := s.sweepTransportOrigins(true); len(released) == 0 {
		t.Fatal("forced sweep released nothing although the cells are dead")
	}
	runtime.KeepAlive(live)
}
