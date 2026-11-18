package interp

// Sprint: #374; Story-ID: 32212c532e3d
//
// Element pointers keep their place in the enclosing array.
//
// A pointer crosses the bridge as the pointee it addresses, and the worker
// gives every origin storage of its own. For a whole variable that is exactly
// Go's model. For &a[i] it is not: Go places element i at a's address plus i
// element sizes, and a dependency can observe that — fmt's %p of &a[1] is
// %p of &a[0] plus the element size (test/fixedbugs/bug260.go).
//
// So a pointer whose path ends inside an array says so. The transport names
// the outermost array that inlines the pointee — the root — by ITS origin and
// type, and spells the remaining path as index and field steps. The worker
// allocates the root once per origin and decodes the pointer as the address
// of that subobject (bashpp_native_worker.go.txt, placedPointer), so sibling
// elements, nested arrays and fields of struct elements are laid out as the
// real Go type lays them out, and a pointer that crosses again has the
// address it had before. Only the addressed subobject is filled and written
// back; the rest of the root is placement, never data.

// bashPPBridgeWithin places a pointer inside the dependency-side storage of
// an enclosing array.
type bashPPBridgeWithin struct {
	// Origin is the session origin of the pointer to the whole root array.
	Origin uint64 `json:"origin"`
	// Type is the root array's bridge type.
	Type string `json:"type"`
	// Steps select the pointee inside the root: an array index, or a field
	// of a struct element.
	Steps []bashPPBridgeWithinStep `json:"steps"`
}

type bashPPBridgeWithinStep struct {
	Field string `json:"field,omitempty"`
	Index int    `json:"index,omitempty"`
}

// bashPPTransportWithin answers the placement of ptr inside its outermost
// inlining array, or nil when its pointee is not inlined in one: a whole
// variable, a slice element (slice backing has its own transport identity), a
// field of a struct that is no array element, anything reached by a
// dereference inside the suffix, or an unsafe view.
func (r *Runner) bashPPTransportWithin(session *bashPPNativeSession, ptr *bashPPPointer) *bashPPBridgeWithin {
	if ptr == nil || len(ptr.path) == 0 || ptr.forged() || ptr.unsafeOffset() != 0 || ptr.unsafeSource() != nil || ptr.unsafeView() != nil {
		return nil
	}
	// Walk the path from the pointee outwards. A field step is inlined in
	// its struct and an index step in its array; a dereference or a slice
	// index leaves the storage, so nothing above it places the pointee.
	root := -1
	var rootMeta *bashPPCollectionMeta
	for i := len(ptr.path) - 1; i >= 0; i-- {
		step := ptr.path[i]
		if step.deref {
			break
		}
		if step.field != "" {
			continue
		}
		parent := *ptr
		parent.path = ptr.path[:i]
		_, meta, _, err := parent.read()
		if err != nil || meta == nil || meta.kind != "array" || meta.typ == nil {
			break
		}
		root, rootMeta = i, meta
	}
	if root < 0 {
		return nil
	}
	steps := make([]bashPPBridgeWithinStep, 0, len(ptr.path)-root)
	for _, step := range ptr.path[root:] {
		steps = append(steps, bashPPBridgeWithinStep{Field: step.field, Index: step.index})
	}
	// The root is named as a pointer to the whole array is named: a root at
	// the cell itself has the nil path &a has.
	rootPtr := &bashPPPointer{target: ptr.target, elem: rootMeta.typ}
	if root > 0 {
		rootPtr.path = append([]bashPPPointerStep(nil), ptr.path[:root]...)
	}
	return &bashPPBridgeWithin{
		Origin: bashPPTransportOrigin(session, rootPtr),
		Type:   r.bashPPBridgeTypeIdentity(rootMeta.typ),
		Steps:  steps,
	}
}
