// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"reflect"
	"unsafe"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// A slice payload is a []any whose own len and cap stand in for the Go slice's
// len and cap. That works for len, but not for cap: Go's append picks the new
// capacity from the *element* type's size, so `append` on a []int that has
// outgrown its array reports a capacity a []any could never reproduce — the
// Tour's append.go prints cap=6 where a []any grows to 5.
//
// bashPPGoShapeType maps a Bash++ element type onto a Go type with the same
// memory shape, so the real runtime can answer the capacity question itself
// (see bashPPSliceGrowCap). It is a shape, not a representation: only size,
// alignment and pointer-ness matter, never the values stored in it.
//
// The second result is false when the shape cannot be established, in which
// case callers keep the []any payload's own growth rather than guessing.
func (r *Runner) bashPPGoShapeType(typ syntax.BashPPTypeExpr, depth int) (reflect.Type, bool) {
	if typ == nil || depth > 8 {
		return nil, false
	}
	if _, ok := r.bashPPInterfaceType(typ); ok {
		return reflect.TypeFor[any](), true
	}
	switch x := r.bashPPUnderlyingType(typ).(type) {
	case *syntax.BashPPNamedType:
		switch x.Name.Value {
		case "bool":
			return reflect.TypeFor[bool](), true
		case "string":
			return reflect.TypeFor[string](), true
		case "int8", "uint8", "byte":
			return reflect.TypeFor[int8](), true
		case "int16", "uint16":
			return reflect.TypeFor[int16](), true
		case "int32", "uint32", "rune":
			return reflect.TypeFor[int32](), true
		case "int64", "uint64", "int", "uint", "uintptr":
			return reflect.TypeFor[int64](), true
		case "float32":
			return reflect.TypeFor[float32](), true
		case "float64":
			return reflect.TypeFor[float64](), true
		case "complex64":
			return reflect.TypeFor[complex64](), true
		case "complex128":
			return reflect.TypeFor[complex128](), true
		}
	case *syntax.BashPPPointerType, *syntax.BashPPChanType, *syntax.BashPPFuncType:
		return reflect.TypeFor[unsafe.Pointer](), true
	case *syntax.BashPPCollectionType:
		elem, ok := r.bashPPGoShapeType(x.Element, depth+1)
		if !ok {
			return nil, false
		}
		switch x.Kind {
		case "map":
			return reflect.TypeFor[unsafe.Pointer](), true
		case "slice":
			return reflect.SliceOf(elem), true
		case "array":
			n, err := r.bashPPArrayLength(x.Length.Value)
			if err != nil || n < 0 {
				return nil, false
			}
			return reflect.ArrayOf(n, elem), true
		}
	case *syntax.BashPPStructType:
		fields, _, ok := r.bashPPStructFields(x)
		if !ok {
			return nil, false
		}
		// reflect.StructOf refuses unexported field names, and the shape only
		// needs the layout, so each field is contributed under an exported
		// placeholder name in declaration order.
		var shape []reflect.StructField
		for i, field := range bashPPFlatFields(fields) {
			elem, ok := r.bashPPGoShapeType(field.typ, depth+1)
			if !ok {
				return nil, false
			}
			shape = append(shape, reflect.StructField{Name: "F" + string(rune('A'+i%26)) + string(rune('A'+i/26)), Type: elem})
		}
		return reflect.StructOf(shape), true
	}
	return nil, false
}

// bashPPSliceGrowCap answers the capacity Go's own append would produce for a
// slice of elem that grows from oldLen/oldCap by added elements. It asks the
// runtime rather than reimplementing growslice's size-class rounding, so the
// answer stays exact across Go releases. A shape we cannot build falls back to
// the []any payload's own growth, which is what callers did before.
func (r *Runner) bashPPSliceGrowCap(elem syntax.BashPPTypeExpr, oldLen, oldCap, added int) (int, bool) {
	shape, ok := r.bashPPGoShapeType(elem, 0)
	if !ok {
		return 0, false
	}
	if shape.Size() == 0 {
		// growslice allocates nothing for a zero-size element and sets the
		// capacity to the new length exactly.
		return oldLen + added, true
	}
	grown, ok := bashPPReflectGrow(shape, oldLen, oldCap, added)
	if !ok || grown < oldLen+added {
		return 0, false
	}
	return grown, true
}

// bashPPReflectGrow performs the probe append on a real slice of shape. It is
// guarded because reflect.MakeSlice panics on lengths the host cannot allocate,
// and an unanswerable capacity must degrade rather than abort the program.
func bashPPReflectGrow(shape reflect.Type, oldLen, oldCap, added int) (grown int, ok bool) {
	defer func() {
		if recover() != nil {
			grown, ok = 0, false
		}
	}()
	sliceType := reflect.SliceOf(shape)
	probe := reflect.MakeSlice(sliceType, oldLen, oldCap)
	probe = reflect.AppendSlice(probe, reflect.MakeSlice(sliceType, added, added))
	return probe.Cap(), true
}

// bashPPZeroSpareCapacity gives a freshly allocated slice payload's unused
// capacity the element type's zero value.
//
// Go's backing arrays are fully zeroed, so re-slicing past the current length —
// `b := make([]int, 0, 5)` then `b[:2]` — must expose typed zeros. A []any
// allocated by make or grown by append leaves those slots holding an untyped
// nil instead, which reaches the dependency bridge as
// "unsupported interpreter collection value <nil>".
//
// It must only be called on a backing array this runner has just allocated.
// Filling the spare capacity of an existing slice would clobber live elements
// of another slice sharing the same array, since a re-slice's capacity runs to
// the end of that array rather than to the end of its own view.
func (r *Runner) bashPPZeroSpareCapacity(seq []any, metas []*bashPPCollectionMeta, elem syntax.BashPPTypeExpr) {
	if cap(seq) == len(seq) || cap(metas) != cap(seq) {
		return
	}
	spare, spareMetas := seq[:cap(seq)], metas[:cap(metas)]
	for i := len(seq); i < len(spare); i++ {
		spare[i], spareMetas[i] = r.bashPPZeroValue(elem)
	}
}

// String conversion and append are distinct operations. Use a real escaping
// primitive conversion for the runtime allocation, not append's growth policy.
// Constant byte strings use the compiler's exact-length array lowering.
func bashPPStringConversionCapacity(text, kind string, stringConstant bool) int {
	if kind == "byte" {
		if stringConstant {
			return len(text)
		}
		return cap(bashPPHeapStringBytes(text))
	}
	return cap(bashPPHeapStringRunes(text))
}

// A noinline result escapes its defining frame. These contain no original
// expressions or source bodies, only the primitive conversion of input data.
//
//go:noinline
func bashPPHeapStringBytes(text string) []byte { return []byte(text) }

//go:noinline
func bashPPHeapStringRunes(text string) []rune { return []rune(text) }

// Allocate fresh, typed backing storage; never zero a retained alias's spare
// capacity. The caller supplies the capacity of the actual conversion route.
func (r *Runner) bashPPConvertedStringStorage(elem syntax.BashPPTypeExpr, n, capacity int) ([]any, []*bashPPCollectionMeta) {
	seq := make([]any, n, capacity)
	metas := make([]*bashPPCollectionMeta, n, capacity)
	r.bashPPZeroSpareCapacity(seq, metas, elem)
	return seq, metas
}

// bashPPAppendSlice appends values to a slice payload with Go's own append
// semantics: it reuses the backing array while the capacity allows, and
// otherwise moves to a freshly allocated one whose capacity is the one Go would
// have chosen for this element type.
//
// The metadata sequence is grown in lockstep and to the same capacity, so that
// re-slicing a payload never outruns its metadata, and so that elements shared
// through the retained array stay aliased by both.
func (r *Runner) bashPPAppendSlice(seq []any, metas []*bashPPCollectionMeta, elem syntax.BashPPTypeExpr, values []any, children []*bashPPCollectionMeta) ([]any, []*bashPPCollectionMeta) {
	oldLen, newLen := len(seq), len(seq)+len(values)
	if newLen <= cap(seq) && newLen <= cap(metas) {
		seq, metas = seq[:newLen], metas[:newLen]
		copy(seq[oldLen:], values)
		copy(metas[oldLen:], children)
		return seq, metas
	}
	newCap := newLen
	if grown, ok := r.bashPPSliceGrowCap(elem, oldLen, cap(seq), len(values)); ok {
		newCap = grown
	} else {
		// No shape for this element: keep the payload's own growth, which is
		// what append did before capacity was modelled.
		probe := append(append([]any(nil), seq...), values...)
		newCap = cap(probe)
	}
	grownSeq := make([]any, newLen, newCap)
	grownMetas := make([]*bashPPCollectionMeta, newLen, newCap)
	copy(grownSeq, seq)
	copy(grownMetas, metas)
	copy(grownSeq[oldLen:], values)
	copy(grownMetas[oldLen:], children)
	r.bashPPZeroSpareCapacity(grownSeq, grownMetas, elem)
	return grownSeq, grownMetas
}

// bashPPAppendTypedSlice is the storage-level append operation for a typed
// slice. Ordinary elements retain the dense carrier and therefore retain the
// exact values (including callable handles) that bashPPAppendSlice stores.
// A logical carrier is accepted only for a proven zero-size element type; its
// elements have no value state to store, so only the Go slice header changes.
//
// additional is separate from len(values) because a spread from a logical
// zero-size slice can contribute many elements without materialising any
// carrier values. false reports the growslice length-overflow condition; the
// builtin boundary must raise Go's "runtime error: growslice: len out of
// range" panic so recover observes the usual runtime.Error value.
func (r *Runner) bashPPAppendTypedSlice(seq []any, meta *bashPPCollectionMeta, elem syntax.BashPPTypeExpr, additional int, values []any, children []*bashPPCollectionMeta) ([]any, *bashPPCollectionMeta, bool) {
	oldLen := bashPPSequenceLen(meta, seq)
	if additional < 0 || additional > int(^uint(0)>>1)-oldLen {
		return seq, meta, false
	}
	if bashPPLogicalSequence(meta) {
		shape, ok := r.bashPPGoShapeType(elem, 0)
		if !ok || shape.Size() != 0 || len(values) != 0 || len(children) != 0 {
			return seq, meta, false
		}
		newLen := oldLen + additional
		newCap := bashPPSequenceCap(meta, seq)
		if newLen > newCap {
			// Go's growslice uses cap=newLen for zero-size elements.
			newCap = newLen
		}
		bashPPSetSequenceShape(meta, newLen, newCap)
		return seq, meta, true
	}
	if additional != len(values) || len(values) != len(children) {
		return seq, meta, false
	}
	seq, meta.sequence = r.bashPPAppendSlice(seq, meta.sequence, elem, values, children)
	bashPPSetSequenceShape(meta, len(seq), cap(seq))
	return seq, meta, true
}

// bashPPNilCollectionBridge describes a nil slice or map to the dependency
// bridge. A nil slice keeps its declared type and prints as the empty slice it
// compares equal to nil about, so it crosses as its own kind with no elements
// rather than as an untyped nil the bridge cannot type.
func (r *Runner) bashPPNilCollectionBridge(meta *bashPPCollectionMeta, typ syntax.BashPPTypeExpr) (bashPPBridgeValue, bool) {
	if meta == nil {
		return bashPPBridgeValue{}, false
	}
	if meta.typ != nil {
		typ = meta.typ
	}
	switch meta.kind {
	case "slice", "array", "inferred-array":
		kind := meta.kind
		if kind == "inferred-array" {
			kind = "array"
		}
		return bashPPBridgeValue{Kind: kind, Type: r.bashPPBridgeTypeIdentity(typ), Elements: []bashPPBridgeValue{}}, true
	case "map":
		return bashPPBridgeValue{Kind: "map", Type: r.bashPPBridgeTypeIdentity(typ), Entries: []bashPPBridgeEntry{}}, true
	}
	return bashPPBridgeValue{}, false
}

// bashPPStructuredBridgeRead resolves a structured read — xs[i], xs[1:], v.F,
// *p — that is owned by the interpreter rather than by a dependency, so that
// passing it to an imported function crosses the value it is instead of failing
// the scalar evaluator with "indexed value is not a scalar".
//
// A scalar read has no metadata and reports false, leaving the scalar path and
// its diagnostics untouched.
func (r *Runner) bashPPStructuredBridgeRead(expr syntax.BashPPExpr) (any, *bashPPCollectionMeta, bool) {
	switch x := expr.(type) {
	case *syntax.BashPPIndexExpr, *syntax.BashPPSliceExpr, *syntax.BashPPSelectorExpr, *syntax.BashPPDerefExpr:
	case *syntax.BashPPConvertExpr:
		// `hash.Write([]byte(s))`: a conversion to a byte or rune slice builds
		// the collection it names, so it crosses as one. A scalar conversion is
		// not claimed and keeps the scalar path, which also owns its errors.
		value, meta, handled, err := r.bashPPConvertToCollection(x)
		if !handled || err != nil {
			return nil, nil, false
		}
		return value, meta, true
	default:
		return nil, nil, false
	}
	value, meta, err := r.bashPPReadExpr(expr)
	if err != nil || meta == nil {
		return nil, nil, false
	}
	return value, meta, true
}
