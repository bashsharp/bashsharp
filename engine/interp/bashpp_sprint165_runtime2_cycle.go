package interp

// Sprint: #165; Story: #99; Story-ID: ca559d7ee23d
//
// Cyclic pointees cross the bridge finitely.
//
// The transport walks a pointer as the pointee it addresses, and a pointee's
// fields as the values they hold. A value graph with a cycle — a doubly
// linked ring whose sentinel's next and prev address the sentinel itself
// (container/list's shape; typeparam/list2.go hands such an element to
// fmt's %p) — made that walk unbounded: the native stack overflowed at the
// 1 GB limit in seconds. Go's own encoders print a nested pointer at depth
// as its address; the bridge's equivalent is a BACK-REFERENCE: a pointer
// whose origin is already on the current walk's path crosses as
// `{Kind:"pointer", Type, Origin}` with no pointee, and the worker binds
// it to the storage it registered for that origin before decoding the
// pointee (pre-order registration in bashpp_native_worker.go.txt). The
// worker's structural encoder answers the same shape for a pointer already
// on its encoding path, and the interpreter rebuilds it as the original
// pointer that origin names. Nothing is normalised: a back-reference to an
// origin the session never registered fails closed on either side.

import (
	"fmt"
	"reflect"
	"strconv"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// bashPPTransportOrigin registers ptr with the session's origin table (or
// finds its existing origin) BEFORE its pointee is transported, so a nested
// reference back to ptr can be spelled by origin. Records do not root
// their cells (see bashpp_native_origin_release.go); unsafe-derived
// pointers take the conservative pinned path instead.
func bashPPTransportOrigin(session *bashPPNativeSession, ptr *bashPPPointer) uint64 {
	session.mu.Lock()
	if session.origins == nil {
		session.origins = map[uint64]*bashPPOriginRecord{}
	}
	// The index answers the storage identity (target cell + path) in O(1).
	// Registered paths are never rewritten in place, so a hit is verified
	// exactly as the former table scan compared, and a miss means no
	// registered pointer names that storage.
	key := bashPPOriginKeyOf(ptr)
	if session.originIndex == nil || session.originIndexed != len(session.origins) {
		session.reindexOriginsLocked()
	}
	if id, ok := session.originIndex[key]; ok {
		if rec := session.origins[id]; rec != nil {
			if live, ok := rec.upgrade(); ok && live.target == ptr.target && reflect.DeepEqual(live.path, ptr.path) {
				session.mu.Unlock()
				return id
			}
		}
	}
	session.originNext++
	id := session.originNext
	rec := newOriginRecord(ptr)
	if originNeedsPin(ptr) {
		rec.strong = ptr
	}
	session.origins[id] = rec
	session.originIndex[key] = id
	session.originIndexed = len(session.origins)
	over := len(session.origins) > bashPPOriginKeepCap
	session.mu.Unlock()
	if over {
		session.sweepTransportOrigins(false)
	}
	return id
}

// bashPPTransportSliceOrigin gives overlapping interpreter slice views a
// stable session-local backing identity and reports the view's element offset.
// A view wholly contained in an already registered live region reuses its
// tightest containing region, including shifted and capacity-restricted views.
// Choosing the tightest match matters once a later wider view is registered:
// map iteration order must not redirect a refresh away from earlier retained
// storage. A later wider view receives a new id because enlarging the worker's
// old slice would relocate and invalidate reflect.Values already selected
// from it. The old interpreter view remains live and refreshable independently.
//
// The table is bounded: a region the worker may reference past the
// introducing call — a slice nested in a pointer pointee, refreshed storage,
// transferred storage — is pinned, and every other region is transient. A
// transient region's worker copy is decoded fresh per call, so once the table
// exceeds sliceRegionKeepCap the transient regions are evicted. A later view
// of an evicted backing simply registers anew; only pinned regions keep a
// stable identity across eviction, which is exactly the retained set.
func bashPPTransportSliceOrigin(session *bashPPNativeSession, view []any, meta *bashPPCollectionMeta, typ syntax.BashPPTypeExpr) (uint64, int) {
	if session == nil || cap(view) == 0 {
		return 0, 0
	}
	data := reflect.ValueOf(view).Pointer()
	if data == 0 {
		return 0, 0
	}
	size := reflect.TypeFor[any]().Size()
	end := data + uintptr(cap(view))*size
	session.mu.Lock()
	defer session.mu.Unlock()
	// The index answers the tightest containing region in O(log n). A
	// table populated outside this function is reindexed before use, so
	// a lookup never misses a registered region.
	if session.sliceRegionRoot == nil || session.sliceRegionCount != len(session.sliceOriginKeep) {
		session.reindexSliceRegionsLocked()
	}
	if bestID, bestStart, ok := sliceRegionLookup(session.sliceRegionRoot, data, end); ok {
		return bestID, int((data - bestStart) / size)
	}
	if session.sliceOriginKeep == nil {
		session.sliceOriginKeep = make(map[uint64]*bashPPNativeSlice)
	}
	session.sliceOriginNext++
	id := session.sliceOriginNext
	session.sliceOriginKeep[id] = &bashPPNativeSlice{view: view[:cap(view)], meta: meta, typ: typ}
	// A slice registered while a pointer pointee is being walked is nested
	// in that transported pointer, so the worker decodes it into persisted
	// storage it keeps past the call; pin it against eviction below.
	if session.slicePinDepth > 0 {
		if session.slicePinned == nil {
			session.slicePinned = make(map[uint64]bool)
		}
		session.slicePinned[id] = true
	}
	session.sliceRegionRoot = sliceRegionInsert(session.sliceRegionRoot, &bashPPSliceRegion{
		start: data,
		end:   end,
		capN:  cap(view),
		id:    id,
		prio:  sliceRegionPriority(id),
	})
	session.sliceRegionCount = len(session.sliceOriginKeep)
	if len(session.sliceOriginKeep) > sliceRegionKeepCap {
		session.evictSliceRegionsLocked()
	}
	return id, 0
}

// sliceRegionKeepCap bounds the session's transported slice regions. The
// table only needs the live transient working set plus the pinned retained
// set; a million round trips with fresh slices must not retain a million
// backings. It comfortably holds the focused identity suites' few thousand
// entries so eviction never disturbs them.
const sliceRegionKeepCap = 8192

// pinSliceRegionLocked marks id as worker-persisted past its introducing
// call, so eviction keeps it. The caller holds session.mu; unknown ids are
// ignored.
func (s *bashPPNativeSession) pinSliceRegionLocked(id uint64) {
	if id == 0 {
		return
	}
	if _, ok := s.sliceOriginKeep[id]; !ok {
		return
	}
	if s.slicePinned == nil {
		s.slicePinned = make(map[uint64]bool)
	}
	s.slicePinned[id] = true
}

// evictSliceRegionsLocked drops transient regions until the table is back
// near half its bound, freeing their backings for collection when the
// interpreter itself holds no alias. Pinned regions — the worker-persisted
// retained set — are never dropped. The caller holds session.mu.
func (s *bashPPNativeSession) evictSliceRegionsLocked() {
	for id, kept := range s.sliceOriginKeep {
		if len(s.sliceOriginKeep) <= sliceRegionKeepCap/2 {
			break
		}
		if s.slicePinned[id] {
			continue
		}
		start := uintptr(0)
		if kept != nil && cap(kept.view) > 0 {
			start = reflect.ValueOf(kept.view).Pointer()
		}
		delete(s.sliceOriginKeep, id)
		delete(s.slicePinned, id)
		if start != 0 {
			s.sliceRegionRoot = sliceRegionDelete(s.sliceRegionRoot, start, id)
		}
	}
	s.sliceRegionCount = len(s.sliceOriginKeep)
}

// bashPPSliceRegion is one node of the backing-interval index over
// sliceOriginKeep. The tree orders regions by (start, id) and each subtree
// remembers its maximum end, so a containment query visits only regions
// that can still contain the transported view. The tree mirrors the
// keep-live table: inserts add regions and eviction deletes them.
type bashPPSliceRegion struct {
	start, end uintptr
	capN       int
	id         uint64
	prio       uint64
	maxEnd     uintptr
	left       *bashPPSliceRegion
	right      *bashPPSliceRegion
}

// sliceRegionPriority decorrelates heap order from allocation order, which
// follows backing addresses closely enough to degenerate an unbalanced
// tree. It is a pure function of the id so a rebuild yields the same shape
// for the same table.
func sliceRegionPriority(id uint64) uint64 {
	id ^= id >> 30
	id *= 0xbf58476d1ce4e5b9
	id ^= id >> 27
	id *= 0x94d049bb133111eb
	id ^= id >> 31
	return id
}

func sliceRegionUpdate(n *bashPPSliceRegion) {
	n.maxEnd = n.end
	if n.left != nil && n.left.maxEnd > n.maxEnd {
		n.maxEnd = n.left.maxEnd
	}
	if n.right != nil && n.right.maxEnd > n.maxEnd {
		n.maxEnd = n.right.maxEnd
	}
}

func sliceRegionRotateRight(n *bashPPSliceRegion) *bashPPSliceRegion {
	left := n.left
	n.left = left.right
	left.right = n
	sliceRegionUpdate(n)
	sliceRegionUpdate(left)
	return left
}

func sliceRegionRotateLeft(n *bashPPSliceRegion) *bashPPSliceRegion {
	right := n.right
	n.right = right.left
	right.left = n
	sliceRegionUpdate(n)
	sliceRegionUpdate(right)
	return right
}

// sliceRegionInsert adds a region to the index.
func sliceRegionInsert(root, node *bashPPSliceRegion) *bashPPSliceRegion {
	if root == nil {
		node.maxEnd = node.end
		return node
	}
	if node.start < root.start || node.start == root.start && node.id < root.id {
		root.left = sliceRegionInsert(root.left, node)
		if root.left.prio < root.prio {
			root = sliceRegionRotateRight(root)
		}
	} else {
		root.right = sliceRegionInsert(root.right, node)
		if root.right.prio < root.prio {
			root = sliceRegionRotateLeft(root)
		}
	}
	sliceRegionUpdate(root)
	return root
}

// sliceRegionDelete removes the region keyed by (start, id) from the index,
// keeping the heap order and every subtree maximum exact. Eviction is the
// only remover.
func sliceRegionDelete(root *bashPPSliceRegion, start uintptr, id uint64) *bashPPSliceRegion {
	if root == nil {
		return nil
	}
	if start == root.start && id == root.id {
		return sliceRegionMerge(root.left, root.right)
	}
	if start < root.start || start == root.start && id < root.id {
		root.left = sliceRegionDelete(root.left, start, id)
	} else {
		root.right = sliceRegionDelete(root.right, start, id)
	}
	sliceRegionUpdate(root)
	return root
}

// sliceRegionMerge joins two indexes whose keys are all below (left) and
// above (right) the removed region, preserving both orders.
func sliceRegionMerge(left, right *bashPPSliceRegion) *bashPPSliceRegion {
	if left == nil {
		return right
	}
	if right == nil {
		return left
	}
	if left.prio < right.prio {
		left.right = sliceRegionMerge(left.right, right)
		sliceRegionUpdate(left)
		return left
	}
	right.left = sliceRegionMerge(left, right.left)
	sliceRegionUpdate(right)
	return right
}

// sliceRegionLookup reports the tightest region containing [data, end):
// the smallest capacity, then the lowest id — exactly the minimum the
// former table scan computed, independent of map iteration order.
func sliceRegionLookup(root *bashPPSliceRegion, data, end uintptr) (uint64, uintptr, bool) {
	var bestID uint64
	var bestStart uintptr
	bestCapacity := 0
	found := false
	stack := []*bashPPSliceRegion{root}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if n == nil || n.maxEnd < end {
			continue
		}
		if n.start > data {
			stack = append(stack, n.left)
			continue
		}
		if n.end >= end && (!found || n.capN < bestCapacity || n.capN == bestCapacity && n.id < bestID) {
			bestID, bestStart, bestCapacity, found = n.id, n.start, n.capN, true
		}
		stack = append(stack, n.left, n.right)
	}
	return bestID, bestStart, found
}

// reindexSliceRegionsLocked rebuilds the interval index from the keep-live
// table. Degenerate entries carry no identity and stay out, exactly as the
// former scan skipped them.
func (s *bashPPNativeSession) reindexSliceRegionsLocked() {
	s.sliceRegionRoot = nil
	size := reflect.TypeFor[any]().Size()
	for id, kept := range s.sliceOriginKeep {
		if kept == nil || cap(kept.view) == 0 {
			continue
		}
		start := reflect.ValueOf(kept.view).Pointer()
		s.sliceRegionRoot = sliceRegionInsert(s.sliceRegionRoot, &bashPPSliceRegion{
			start: start,
			end:   start + uintptr(cap(kept.view))*size,
			capN:  cap(kept.view),
			id:    id,
			prio:  sliceRegionPriority(id),
		})
	}
	s.sliceRegionCount = len(s.sliceOriginKeep)
}

// bashPPOriginKey is the storage identity a transport origin names: the root
// cell and an exact spelling of the step path. A nil path and an empty path
// spell differently, as reflect.DeepEqual distinguishes them.
type bashPPOriginKey struct {
	target *bashPPCell
	path   string
}

func bashPPOriginKeyOf(ptr *bashPPPointer) bashPPOriginKey {
	if ptr.path == nil {
		return bashPPOriginKey{target: ptr.target}
	}
	b := make([]byte, 0, 1+len(ptr.path)*8)
	b = append(b, '[')
	for _, step := range ptr.path {
		// Length-prefixed field names keep arbitrary names unambiguous.
		b = strconv.AppendInt(b, int64(len(step.field)), 10)
		b = append(b, ':')
		b = append(b, step.field...)
		b = append(b, ',')
		b = strconv.AppendInt(b, int64(step.index), 10)
		if step.deref {
			b = append(b, '*')
		}
		b = append(b, ';')
	}
	return bashPPOriginKey{target: ptr.target, path: string(b)}
}

// reindexOriginsLocked rebuilds the identity index from the origin table.
// It keeps the lowest origin for a duplicated identity, which is the only
// one the table registration itself could have produced. Records whose cell
// has died carry no identity and stay out of the index until the next sweep
// removes them; the caller holds session.mu.
func (s *bashPPNativeSession) reindexOriginsLocked() {
	s.originIndex = make(map[bashPPOriginKey]uint64, len(s.origins))
	for id, rec := range s.origins {
		if rec == nil {
			continue
		}
		live, ok := rec.upgrade()
		if !ok {
			continue
		}
		key := bashPPOriginKeyOf(live)
		if prev, ok := s.originIndex[key]; !ok || id < prev {
			s.originIndex[key] = id
		}
	}
	s.originIndexed = len(s.origins)
}

// bashPPTransportEnter marks origin as being transported on this runner's
// current walk. It answers true when origin is already on the path — the
// caller then sends a back-reference — and otherwise a leave function that
// unmarks it once its pointee has crossed.
func (r *Runner) bashPPTransportEnter(origin uint64) (onPath bool, leave func()) {
	if r.bashPPTransportPath == nil {
		r.bashPPTransportPath = map[uint64]bool{}
	}
	if r.bashPPTransportPath[origin] {
		return true, nil
	}
	r.bashPPTransportPath[origin] = true
	return false, func() {
		delete(r.bashPPTransportPath, origin)
		if len(r.bashPPTransportPath) == 0 {
			r.bashPPTransportPath = nil
		}
	}
}

// bashPPTransportBackReference is the finite spelling of a pointer already
// on the transport path: its type from the pointee's declared type, its
// origin, no pointee.
func bashPPTransportBackReference(session *bashPPNativeSession, origin uint64, meta *bashPPCollectionMeta, typ syntax.BashPPTypeExpr) bashPPBridgeValue {
	if meta != nil && meta.typ != nil {
		typ = meta.typ
	}
	return bashPPBridgeValue{Origin: origin, Session: session.id, Kind: "pointer", Type: "*" + bashPPBridgeTypeText(typ)}
}

// bashPPBridgeBackReference answers whether v is a back-reference the
// dependency sent (a pointer with an origin and no pointee).
func bashPPBridgeBackReference(v bashPPBridgeValue) bool {
	return v.Kind == "pointer" && len(v.Elements) == 0
}

// bashPPBridgeBackReferencePointer resolves a back-reference the dependency
// sent to the original pointer its origin names; an origin this session
// never registered fails closed.
func (r *Runner) bashPPBridgeBackReferencePointer(v bashPPBridgeValue) (*bashPPPointer, error) {
	session := r.bashPPTools.bridge
	if session == nil || v.Origin == 0 || v.Session != session.id {
		return nil, fmt.Errorf("gosource: pointer back-reference names no origin of this dependency session")
	}
	ptr, ok := session.originLookup(v.Origin)
	if !ok || ptr == nil {
		return nil, fmt.Errorf("gosource: pointer back-reference names an unknown origin")
	}
	return ptr, nil
}

// bashPPBridgeOriginPointer resolves a pointer-typed value the dependency
// sent to the original pointer its origin names. A back-reference (no
// pointee) MUST resolve; a flattened pointee carrying an origin resolves when
// the origin is this session's, and otherwise stays the copy-by-shape the
// caller rebuilds.
func (r *Runner) bashPPBridgeOriginPointer(v bashPPBridgeValue) (*bashPPPointer, bool, error) {
	if bashPPBridgeBackReference(v) {
		ptr, err := r.bashPPBridgeBackReferencePointer(v)
		return ptr, err == nil, err
	}
	session := r.bashPPTools.bridge
	if session == nil || v.Origin == 0 || v.Session != session.id {
		return nil, false, nil
	}
	ptr, ok := session.originLookup(v.Origin)
	if !ok || ptr == nil {
		// A flattened pointee for a released origin is a resurface, not an
		// alias: the caller rebuilds it as a fresh copy. Back-references
		// (no pointee) fail closed in their own resolver above.
		return nil, false, nil
	}
	return ptr, true, nil
}
