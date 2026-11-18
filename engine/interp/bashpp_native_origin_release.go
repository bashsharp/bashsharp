package interp

import (
	"fmt"
	"os"
	"runtime"
	"weak"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Sprint: #374; Story: #1540; Story-ID: 61179c258609
//
// Transported pointer origins are released when their storage dies.
//
// The origin table used to root every transported cell for the whole
// session, so sequential work (one corpus row after another, one dead
// buffer after runtime.GC) accumulated every row's storage forever. Three
// earlier candidates failed their ownership probes: a cleanup on the
// owning cell could never fire while the table rooted the cell, a weak
// wrapper tracked the wrong object (wrapper reachability is not storage
// reachability), and call-scoped removal split identity from a natively
// retained pointer.
//
// This mechanism combines the lessons of all three:
//
//   - The table no longer roots cells. Each record keeps a weak pointer to
//     its cell plus an immutable snapshot of the metadata needed to
//     materialise the *bashPPPointer on use. Cell death is therefore
//     observable, and it tracks storage rather than wrappers.
//   - Release is two-sided. A sweep proposes origins whose cells are dead;
//     the proposal rides the next real request as OriginReleases, and the
//     worker prunes its values, identity indexes, sent snapshots, retained
//     set and array placements before dispatching that request. Either
//     side recreates its half harmlessly if the origin is named again with
//     a full pointee: origin IDs are never reused.
//   - The conservative path stays for everything the bridge cannot prove
//     transient: uintptr-escaped addresses and unsafe-derived pointers are
//     pinned strongly at registration, and origins named by open requests
//     are held strongly until their reply is applied. A worker writeback
//     for an origin that once existed but is gone is dropped (its target
//     is unobservable); a writeback for an origin that never existed still
//     fails closed.
//
// Safety boundary: a native object that retains a transported pointer
// without ever sending it back across the bridge is invisible to both
// sides. Releasing such an origin is unobservable unless native code later
// resurfaces the pointer AND the program identity-compares or
// cross-mutates the resurfaced copy against interpreter state. Plain
// single-call transports (the corpus leak shape) never do this; callbacks,
// reflected values and uintptr escapes keep the conservative path.

// bashPPOriginKeepCap bounds the session's transported pointer origins.
// Past the cap, registration clears the identity index, collects, and
// releases dead cells instead of growing the table, so sequential work
// (corpus rows, repeated allocations) forms a sawtooth rather than a
// leak. The collection is amortized over cap transports; bridged calls
// already cross a process boundary, so one GC per cap transports is
// negligible beside the call cost. It is a variable (not a constant) so
// focused tests can force the sweep path.
var bashPPOriginKeepCap = 1024

// bashPPOriginRecord is one transport origin that does not root its cell.
// strong is non-nil only for pinned (conservative-path) origins; every
// other record names its storage weakly and materialises the pointer on
// upgrade while the cell lives.
type bashPPOriginRecord struct {
	cell           weak.Pointer[bashPPCell]
	path           []bashPPPointerStep
	elem           syntax.BashPPTypeExpr
	storageAddress *byte
	strong         *bashPPPointer
}

// bashPPOriginHold is one open request's claim on an origin: a refcount
// plus the strong pointer that keeps the cell alive while the request is
// in flight, so storage cannot die mid-request under a pending writeback.
type bashPPOriginHold struct {
	count  int
	strong *bashPPPointer
}

// newOriginRecord snapshots ptr without rooting its target. The path is
// cloned (preserving nil versus empty, which spell differently) so later
// appends through the live pointer cannot rewrite the registered steps.
func newOriginRecord(ptr *bashPPPointer) *bashPPOriginRecord {
	rec := &bashPPOriginRecord{elem: ptr.elem, storageAddress: ptr.storageAddress}
	if ptr.target != nil {
		rec.cell = weak.Make(ptr.target)
	}
	if ptr.path == nil {
		rec.path = nil
	} else if len(ptr.path) == 0 {
		rec.path = []bashPPPointerStep{}
	} else {
		rec.path = append([]bashPPPointerStep(nil), ptr.path...)
	}
	return rec
}

// upgrade materialises the registered pointer while its cell lives. The
// returned path is a fresh clone, so callers may derive without corrupting
// the record.
func (rec *bashPPOriginRecord) upgrade() (*bashPPPointer, bool) {
	if rec == nil {
		return nil, false
	}
	if rec.strong != nil {
		return rec.strong, true
	}
	cell := rec.cell.Value()
	if cell == nil {
		return nil, false
	}
	out := &bashPPPointer{target: cell, elem: rec.elem, storageAddress: rec.storageAddress}
	if rec.path == nil {
		out.path = nil
	} else if len(rec.path) == 0 {
		out.path = []bashPPPointerStep{}
	} else {
		out.path = append([]bashPPPointerStep(nil), rec.path...)
	}
	return out, true
}

// originNeedsPin answers whether ptr must take the conservative path:
// anything derived through unsafe or forged addressing can be retained by
// native code invisibly (notably via an escaped integer address), so its
// origin is never proposed for release.
func originNeedsPin(ptr *bashPPPointer) bool {
	return ptr.forged() || ptr.unsafeAddress() != 0 || ptr.unsafeOffset() != 0 ||
		ptr.unsafeView() != nil || ptr.unsafeOverlay() != nil || ptr.unsafeSlice() != nil ||
		ptr.unsafeSource() != nil || ptr.unsafeRefusal() != nil
}

// originLookup resolves id to its live pointer. It answers false for both
// unknown origins and records whose cell has died; use originExpired to
// tell a released origin (safe to ignore) from a bogus one (fail closed).
func (s *bashPPNativeSession) originLookup(id uint64) (*bashPPPointer, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.origins[id].upgrade()
}

// originExpired answers whether id once named an origin of this session.
// Origin IDs are never reused, so an unknown id at or below the high-water
// mark is a released origin, while anything above it never existed.
func (s *bashPPNativeSession) originExpired(id uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return id > 0 && id <= s.originNext
}

// pinTransportOrigin moves id to the conservative path, rooting its cell
// for the rest of the session. It is a no-op for unknown or dead origins.
func (s *bashPPNativeSession) pinTransportOrigin(id uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec := s.origins[id]
	if rec == nil || rec.strong != nil {
		return
	}
	if live, ok := rec.upgrade(); ok {
		rec.strong = live
	}
}

// sweepTransportOrigins proposes dead origins for release. Records that
// are pinned, held by open requests, or still upgradeable stay; the rest
// leave the table and queue a worker release drained by the next request.
// The index is rebuilt from the survivors. It returns the released IDs.
func (s *bashPPNativeSession) sweepTransportOrigins(force bool) []uint64 {
	s.mu.Lock()
	if !force && (len(s.origins) <= bashPPOriginKeepCap || len(s.origins) < s.originSweepNext) {
		s.mu.Unlock()
		return nil
	}
	// The identity index keys root cells strongly; drop it so dead cells
	// are collectible, then observe what actually died.
	s.originIndex = nil
	s.mu.Unlock()
	runtime.GC()
	s.mu.Lock()
	defer s.mu.Unlock()
	dbg := os.Getenv("S374_ORIGIN_DEBUG") != ""
	var released []uint64
	liveCount := 0
	for id, rec := range s.origins {
		if rec == nil || rec.strong != nil {
			continue
		}
		if _, open := s.originOpen[id]; open {
			continue
		}
		if _, ok := rec.upgrade(); ok {
			liveCount++
			continue
		}
		delete(s.origins, id)
		released = append(released, id)
	}
	if dbg {
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		fmt.Fprintf(os.Stderr, "s374sweep: table=%d released=%d live=%d hostalloc=%d\n",
			len(s.origins)+len(released), len(released), liveCount, ms.Alloc)
	}
	if len(released) > 0 {
		s.originReleaseQueue = append(s.originReleaseQueue, released...)
	}
	s.reindexOriginsLocked()
	// Amortise: the survivors are live, so the next unforced sweep waits
	// until the table has doubled past them.
	s.originSweepNext = 2 * len(s.origins)
	return released
}

// takeOriginReleases drains queued worker releases except the excluded
// origins, which are named by the request about to carry this batch.
func (s *bashPPNativeSession) takeOriginReleases(exclude map[uint64]bool) []uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.originReleaseQueue) == 0 {
		return nil
	}
	if len(exclude) == 0 {
		out := s.originReleaseQueue
		s.originReleaseQueue = nil
		return out
	}
	var out, rest []uint64
	for _, id := range s.originReleaseQueue {
		if exclude[id] {
			rest = append(rest, id)
		} else {
			out = append(out, id)
		}
	}
	s.originReleaseQueue = rest
	return out
}

// openOriginRequest claims every origin q names until the reply is
// applied, and drains the release queue around those claims so a release
// can never overtake an open request naming the same origin.
func (s *bashPPNativeSession) openOriginRequest(qSet map[uint64]bool) []uint64 {
	s.mu.Lock()
	if s.originOpen == nil && len(qSet) > 0 {
		s.originOpen = make(map[uint64]*bashPPOriginHold, len(qSet))
	}
	for id := range qSet {
		hold := s.originOpen[id]
		if hold == nil {
			hold = &bashPPOriginHold{}
			s.originOpen[id] = hold
		}
		hold.count++
		if hold.strong == nil {
			hold.strong, _ = s.origins[id].upgrade()
		}
	}
	exclude := make(map[uint64]bool, len(s.originOpen))
	for id := range s.originOpen {
		exclude[id] = true
	}
	s.mu.Unlock()
	return s.takeOriginReleases(exclude)
}

// closeOriginRequest drops one send's claims from the open set.
func (s *bashPPNativeSession) closeOriginRequest(qSet map[uint64]bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id := range qSet {
		hold := s.originOpen[id]
		if hold == nil {
			continue
		}
		hold.count--
		if hold.count <= 0 {
			delete(s.originOpen, id)
		}
	}
}

// collectBridgeOrigins gathers every origin a request names: any value
// node's origin (pointers, reflect handles, uintptr-escaped addresses) and
// every element-address placement root.
func collectBridgeOrigins(q *bashPPBridgeRequest) map[uint64]bool {
	var out map[uint64]bool
	var walk func(v *bashPPBridgeValue)
	walk = func(v *bashPPBridgeValue) {
		if v == nil {
			return
		}
		if v.Origin != 0 {
			if out == nil {
				out = map[uint64]bool{}
			}
			out[v.Origin] = true
		}
		if v.Within != nil && v.Within.Origin != 0 {
			if out == nil {
				out = map[uint64]bool{}
			}
			out[v.Within.Origin] = true
		}
		for i := range v.Elements {
			walk(&v.Elements[i])
		}
		for _, f := range v.Fields {
			f := f
			walk(&f)
		}
		for i := range v.Entries {
			walk(&v.Entries[i].Key)
			walk(&v.Entries[i].Value)
		}
		for i := range v.CallArgs {
			walk(&v.CallArgs[i])
		}
	}
	if q.Receiver != nil {
		walk(q.Receiver)
	}
	for i := range q.Args {
		walk(&q.Args[i])
	}
	for i := range q.Values {
		walk(&q.Values[i])
	}
	for i := range q.SliceBuffers {
		walk(&q.SliceBuffers[i].Value)
	}
	return out
}

// sweepBridgeOrigins runs an origin sweep after a source-level collection.
// The collector just ran host GC, so even small tables are worth sweeping:
// this is what lets an explicit runtime.GC drop dead transported buffers
// before the next bridged call carries the releases to the worker.
func (r *Runner) sweepBridgeOrigins() {
	if s := r.bashPPTools.bridge; s != nil {
		s.sweepTransportOrigins(true)
	}
}
