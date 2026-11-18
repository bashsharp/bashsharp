// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"encoding/binary"
	"fmt"
	"go/types"
	"math"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// A slice read through a reinterpreted slice header —
//
//	h := Header{Data: unsafe.Pointer(&b[0]), Len: n, Cap: c}
//	s := *(*[]T)(unsafe.Pointer(&h))
//
// — is answered by this file. Interpreter storage is a []any of whole
// elements, so one []any cannot be two element grids at once. Two cases are
// representable:
//
// Same element type. The result aliases the span Data names, exactly as
// unsafe.Slice does, with the header's own length and capacity.
//
// Different element type (a cross-width view). The bytes of the source
// elements are moved: they are encoded with the gc little-endian layout,
// decoded as elements of T into a fresh backing, and every source slot is
// replaced by a goSourceUnsafeMoved marker naming that backing. The new
// backing is then the one and only home of those bytes, so reads and writes
// through the view, appends within its capacity, and a later reinterpretation
// back to the original type (which moves the bytes again) all observe exactly
// the bytes Go would. Reading the same header again returns the same backing.
//
// What a move cannot represent is the source and the view being used side by
// side: the moved-from slots no longer hold values of their type. A marker is
// not a value of any Go type, so such a use fails rather than reading stale
// bytes, and reinterpreting a moved-from span again is refused by name.
//
// Only layouts whose every byte is an observable value are admitted: integers,
// bool, finite floats, pointers, and arrays and padding-free structs of those.
// Everything else — strings, interfaces, slices, maps, channels, functions,
// dependency-owned types, blank fields, padded structs — is refused with
// BASHPP-EUNSAFE-VIEW before any storage is touched.

// goSourceUnsafeSliceView marks a pointer produced by converting an
// unsafe.Pointer to a slice-header struct into a pointer to a slice type.
type goSourceUnsafeSliceView struct {
	r      *Runner
	source syntax.BashPPTypeExpr // the header struct type
	target syntax.BashPPTypeExpr // the slice type, as spelled
	elem   syntax.BashPPTypeExpr // the slice's element type
	fields [3]string             // Data, Len, Cap field names in declaration order
}

// goSourceUnsafeMove records one completed move of bytes into a view backing.
type goSourceUnsafeMove struct {
	elem  string // element type text of the backing
	full  []any  // the complete backing, len == cap
	metas []*bashPPCollectionMeta
	slots int // number of source slots the move consumed
}

// goSourceUnsafeMoved is what a moved-from storage slot holds.
type goSourceUnsafeMoved struct {
	move  *goSourceUnsafeMove
	index int
}

func (m goSourceUnsafeMoved) String() string {
	return "BASHPP-EUNSAFE-VIEW: storage was moved into a reinterpreted []" + m.move.elem + " view and no longer holds a value"
}

func (m goSourceUnsafeMoved) Error() string { return m.String() }

// goSourceUnsafeSliceHeaderView reports the view a `(*[]T)(p)` conversion
// makes when p's storage is a three-word slice header: a struct of exactly an
// unsafe.Pointer followed by two ints.
func (r *Runner) goSourceUnsafeSliceHeaderView(source, target syntax.BashPPTypeExpr) *goSourceUnsafeSliceView {
	if !r.bashPPGoSource || source == nil || target == nil {
		return nil
	}
	slice, ok := r.bashPPUnderlyingType(target).(*syntax.BashPPCollectionType)
	if !ok || slice.Kind != "slice" || slice.Element == nil {
		return nil
	}
	fields, ok := r.goSourceUnsafeSliceHeaderFields(source)
	if !ok {
		return nil
	}
	view := &goSourceUnsafeSliceView{r: r, source: source, target: target, elem: slice.Element}
	for i, field := range bashPPFlatFields(fields) {
		view.fields[i] = field.name
	}
	return view
}

// goSourceUnsafeSliceHeaderFields authenticates the only imported struct shape
// an interpreted composite literal may materialise locally: the three words of
// a slice header. The declared imported type remains its type identity; only
// this particular value's storage is interpreter-owned.
func (r *Runner) goSourceUnsafeSliceHeaderFields(typ syntax.BashPPTypeExpr) ([]*syntax.BashPPField, bool) {
	fields, ok := r.goSourceUnsafeStructFields(typ)
	if !ok {
		return nil, false
	}
	flat := bashPPFlatFields(fields)
	if len(flat) != 3 || flat[0].name != "Data" || flat[1].name != "Len" || flat[2].name != "Cap" {
		return nil, false
	}
	// Metadata for unsafe.Pointer is represented under the importing source
	// file's alias. The authenticated unsafeheader export can instead arrive
	// here with its package-path spelling, so accept that one well-known
	// pointer-word name after the struct itself and its field order have been
	// authenticated by go/types.
	if !r.goSourceUnsafePointerType(flat[0].typ) && bashPPTypeText(flat[0].typ) != "unsafe.Pointer" {
		return nil, false
	}
	for i, field := range flat {
		if field.name == "_" {
			return nil, false
		}
		if i > 0 {
			named, ok := r.bashPPUnderlyingType(field.typ).(*syntax.BashPPNamedType)
			if !ok || named.Name == nil || named.Name.Value != "int" {
				return nil, false
			}
		}
	}
	return fields, true
}

// goSourceUnsafeStructFields includes imported structs whose authenticated
// shape lives in go/types metadata rather than the interpreted type registry.
// Imported runtime headers (for example internal/unsafeheader.Slice) are
// dependency-owned, but their exported field layout is still safe to inspect
// for the generic three-word slice-header recognition above.
func (r *Runner) goSourceUnsafeStructFields(typ syntax.BashPPTypeExpr) ([]*syntax.BashPPField, bool) {
	if fields, _, ok := r.bashPPStructFields(typ); ok {
		return fields, true
	}
	named, ok := typ.(*syntax.BashPPNamedType)
	if !ok || named.Name == nil || r.bashPPTools.nativeTypes == nil {
		return nil, false
	}
	name, qualifier := named.Name.Value, ""
	if dot := strings.LastIndexByte(name, '.'); dot >= 0 {
		qualifier, name = name[:dot], name[dot+1:]
	}
	// Several imported packages may export a type of the same name: a file
	// importing both internal/unsafeheader and cmd/compile/internal/types sees
	// two types called Slice. The qualifier the source wrote selects the
	// package. It may be an import alias of this session (resolved through
	// the import table, which also covers the synthetic aliases of linked
	// multi-file packages), the import path itself, or the package's base
	// name. Only an unqualified name, or a qualifier that resolves to
	// nothing, falls back to requiring the name to be unique among imports.
	var native types.Type
	if qualifier != "" {
		paths := []string{}
		if path, ok := r.bashPPImports[qualifier]; ok {
			paths = append(paths, path)
		}
		if r.goSourceImportsPath(qualifier) {
			paths = append(paths, qualifier)
		}
		for _, path := range paths {
			if candidate, ok := r.bashPPTools.nativeTypes[path+"."+name]; ok {
				native = candidate
				break
			}
		}
	}
	if native == nil {
		var qualified types.Type
		matches, qualifiedMatches := 0, 0
		for key, candidate := range r.bashPPTools.nativeTypes {
			path := strings.TrimSuffix(key, "."+name)
			if path == key || !r.goSourceImportsPath(path) {
				continue
			}
			native = candidate
			matches++
			if qualifier != "" && strings.HasSuffix(path, "/"+qualifier) {
				qualified = candidate
				qualifiedMatches++
			}
		}
		switch {
		case qualifiedMatches == 1:
			native = qualified
		case matches != 1:
			return nil, false
		}
	}
	structure, ok := types.Unalias(native).Underlying().(*types.Struct)
	if !ok {
		return nil, false
	}
	fields := make([]*syntax.BashPPField, 0, structure.NumFields())
	for i := 0; i < structure.NumFields(); i++ {
		field := structure.Field(i)
		fields = append(fields, &syntax.BashPPField{
			Names:         []*syntax.Lit{{Value: field.Name()}},
			FieldTypeExpr: syntax.BashPPTypeExprFromText(types.TypeString(field.Type(), func(pkg *types.Package) string { return pkg.Name() })),
		})
	}
	return fields, true
}

func goSourceUnsafeViewErr(format string, args ...any) error {
	return fmt.Errorf("BASHPP-EUNSAFE-VIEW: "+format, args...)
}

// read answers the slice a header value describes.
func (v *goSourceUnsafeSliceView) read(header any) (any, *bashPPCollectionMeta, error) {
	r := v.r
	mapping, ok := header.(map[string]any)
	if !ok {
		return nil, nil, goSourceUnsafeViewErr("slice header %s no longer names struct storage (it holds %T)", bashPPTypeText(v.source), header)
	}
	data, _ := bashPPStorageGet(mapping, v.fields[0])
	var words [2]int
	for i := range words {
		stored, _ := bashPPStorageGet(mapping, v.fields[i+1])
		n, ok := goSourceUnsafeStoredInt(stored)
		if !ok {
			return nil, nil, goSourceUnsafeViewErr("slice header field %s holds no integer", v.fields[i+1])
		}
		words[i] = int(int64(n))
	}
	length, capacity := words[0], words[1]
	if length < 0 || capacity < length {
		return nil, nil, goSourceUnsafeViewErr("slice header has invalid len %d and cap %d", length, capacity)
	}
	meta := &bashPPCollectionMeta{kind: "slice", typ: v.target}
	ptr, isPointer := data.(*bashPPPointer)
	if data != nil && !isPointer {
		return nil, nil, goSourceUnsafeViewErr("slice header data (%T) is not an interpreter-owned pointer", data)
	}
	if ptr == nil {
		if capacity != 0 {
			return nil, nil, goSourceUnsafeViewErr("slice header has nil data and cap %d", capacity)
		}
		return []any(nil), meta, nil
	}
	seq, parentMeta, start, err := goSourceUnsafeSpan(ptr)
	if err != nil {
		return nil, nil, err
	}
	if bashPPLogicalSequence(parentMeta) {
		return nil, nil, goSourceUnsafeViewErr("slice header data names storage with no element carrier")
	}
	seq = seq[:cap(seq)]
	source := ptr.unsafeSource()
	if source == nil {
		source = ptr.elem
	}
	if source == nil {
		return nil, nil, goSourceUnsafeViewErr("slice header data has no element type")
	}
	if bashPPTypeText(source) == bashPPTypeText(v.elem) {
		if capacity > len(seq)-start {
			return nil, nil, fmt.Errorf("BASHPP-EUNSAFE-SPAN: slice header cap %d runs past the %d-element storage span", capacity, len(seq)-start)
		}
		end := start + capacity
		if parentMeta != nil && cap(parentMeta.sequence) >= end {
			meta.sequence = parentMeta.sequence[:end][start : start+length : end]
		} else {
			meta.sequence = make([]*bashPPCollectionMeta, length, capacity)
		}
		return seq[start : start+length : end], meta, nil
	}
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		return nil, nil, fmt.Errorf("BASHPP-EUNSAFE-LAYOUT: reinterpreted slice views require a 64-bit little-endian gc layout, got %s", runtime.GOARCH)
	}
	from, err := r.goSourceUnsafeByteLayout(source, 0)
	if err != nil {
		return nil, nil, err
	}
	to, err := r.goSourceUnsafeByteLayout(v.elem, 0)
	if err != nil {
		return nil, nil, err
	}
	if from.padding || to.padding {
		return nil, nil, goSourceUnsafeViewErr("padded layouts %s and %s cannot be moved through a slice view", bashPPTypeText(source), bashPPTypeText(v.elem))
	}
	if from.size == 0 || to.size == 0 {
		return nil, nil, goSourceUnsafeViewErr("zero-size element type cannot be reinterpreted (%s as %s)", bashPPTypeText(source), bashPPTypeText(v.elem))
	}
	if capacity > math.MaxInt/to.size {
		return nil, nil, fmt.Errorf("BASHPP-EUNSAFE-SPAN: slice header cap %d overflows the address space", capacity)
	}
	size := capacity * to.size
	if size%from.size != 0 {
		return nil, nil, fmt.Errorf("BASHPP-EUNSAFE-SPAN: %d bytes of []%s do not cover whole %d-byte %s elements", size, bashPPTypeText(v.elem), from.size, bashPPTypeText(source))
	}
	slots := size / from.size
	if slots > len(seq)-start {
		return nil, nil, fmt.Errorf("BASHPP-EUNSAFE-SPAN: slice header cap %d (%d bytes) runs past the %d-byte storage span", capacity, size, (len(seq)-start)*from.size)
	}
	if capacity == 0 {
		meta.sequence = []*bashPPCollectionMeta{}
		return []any{}, meta, nil
	}
	span := seq[start : start+slots]
	elemText := bashPPTypeText(v.elem)
	if first, ok := span[0].(goSourceUnsafeMoved); ok {
		// The same header read again: hand back the backing its bytes
		// already moved into, provided it is this very view.
		last, _ := span[slots-1].(goSourceUnsafeMoved)
		if first.index == 0 && first.move.elem == elemText && first.move.slots == slots && len(first.move.full) == capacity && last.move == first.move && last.index == slots-1 {
			meta.sequence = first.move.metas[:length:capacity]
			return first.move.full[:length:capacity], meta, nil
		}
	}
	image := &goSourceUnsafeImage{bytes: make([]byte, size)}
	for i, slot := range span {
		if moved, ok := slot.(goSourceUnsafeMoved); ok {
			return nil, nil, goSourceUnsafeViewErr("storage was moved into a reinterpreted []%s view and cannot be read as %s", moved.move.elem, bashPPTypeText(source))
		}
		if err := r.goSourceUnsafeEncode(from, slot, image, i*from.size); err != nil {
			return nil, nil, err
		}
	}
	move := &goSourceUnsafeMove{elem: elemText, full: make([]any, capacity), metas: make([]*bashPPCollectionMeta, capacity), slots: slots}
	for i := range move.full {
		if move.full[i], move.metas[i], err = r.goSourceUnsafeDecode(to, image, i*to.size); err != nil {
			return nil, nil, err
		}
	}
	// Nothing was changed until every byte was encoded and decoded; from here
	// the view's backing owns the bytes.
	for i := range span {
		span[i] = goSourceUnsafeMoved{move: move, index: i}
	}
	meta.sequence = move.metas[:length:capacity]
	return move.full[:length:capacity], meta, nil
}

// goSourceUnsafeLayout is the byte layout of a type every byte of which is an
// observable value.
type goSourceUnsafeLayout struct {
	kind    byte // 'i' integer, 'b' bool, 'f' float, 'p' pointer, 's' struct, 'a' array
	size    int
	align   int
	signed  bool
	typ     syntax.BashPPTypeExpr
	fields  []goSourceUnsafeLayoutField
	elem    *goSourceUnsafeLayout
	count   int
	padding bool
	// blank reports bytes occupied by blank fields, including through nested
	// structs and arrays. Unlike ordinary alignment padding, those bytes are
	// copied by whole-value assignment but have no interpreter field storage.
	blank bool
}

type goSourceUnsafeLayoutField struct {
	name   string
	offset int
	layout *goSourceUnsafeLayout
}

func (r *Runner) goSourceUnsafeByteLayout(typ syntax.BashPPTypeExpr, depth int) (*goSourceUnsafeLayout, error) {
	refuse := func(why string) (*goSourceUnsafeLayout, error) {
		return nil, goSourceUnsafeViewErr("%s %s cannot be reinterpreted through a slice view", why, bashPPTypeText(typ))
	}
	if typ == nil || depth > 8 {
		return refuse("type")
	}
	if r.goSourceUnsafePointerType(typ) {
		return &goSourceUnsafeLayout{kind: 'p', size: 8, align: 8, typ: typ}, nil
	}
	if r.bashPPNativeType(typ) {
		return refuse("dependency-owned type")
	}
	if _, ok := r.bashPPInterfaceType(typ); ok {
		return refuse("interface type")
	}
	switch x := r.bashPPUnderlyingType(typ).(type) {
	case *syntax.BashPPNamedType:
		scalar := func(kind byte, size int, signed bool) (*goSourceUnsafeLayout, error) {
			return &goSourceUnsafeLayout{kind: kind, size: size, align: size, signed: signed, typ: typ}, nil
		}
		switch x.Name.Value {
		case "bool":
			return scalar('b', 1, false)
		case "int8":
			return scalar('i', 1, true)
		case "uint8", "byte":
			return scalar('i', 1, false)
		case "int16":
			return scalar('i', 2, true)
		case "uint16":
			return scalar('i', 2, false)
		case "int32", "rune":
			return scalar('i', 4, true)
		case "uint32":
			return scalar('i', 4, false)
		case "int64", "int":
			return scalar('i', 8, true)
		case "uint64", "uint", "uintptr":
			return scalar('i', 8, false)
		case "float32":
			return scalar('f', 4, false)
		case "float64":
			return scalar('f', 8, false)
		}
		return refuse("type")
	case *syntax.BashPPPointerType:
		return &goSourceUnsafeLayout{kind: 'p', size: 8, align: 8, typ: typ}, nil
	case *syntax.BashPPCollectionType:
		if x.Kind != "array" || x.Length == nil {
			return refuse(x.Kind + " type")
		}
		n, err := r.bashPPArrayLength(x.Length.Value)
		if err != nil || n < 0 {
			return refuse("array type")
		}
		elem, err := r.goSourceUnsafeByteLayout(x.Element, depth+1)
		if err != nil {
			return nil, err
		}
		if n != 0 && elem.size > math.MaxInt32/n {
			return refuse("array type")
		}
		return &goSourceUnsafeLayout{kind: 'a', size: n * elem.size, align: elem.align, typ: typ, elem: elem, count: n, padding: elem.padding, blank: elem.blank}, nil
	case *syntax.BashPPStructType:
		fields, _, ok := r.bashPPStructFields(typ)
		if !ok {
			fields = x.Fields
		}
		flat := bashPPFlatFields(fields)
		goType, layoutOK := r.goSourceLayoutType(typ, map[string]bool{})
		var goStruct *types.Struct
		if layoutOK {
			goStruct, _ = goType.Underlying().(*types.Struct)
		}
		structOK := goStruct != nil
		sizes := types.SizesFor("gc", runtime.GOARCH)
		if !layoutOK || !structOK || sizes == nil || goStruct.NumFields() != len(flat) {
			return refuse("layout of")
		}
		offsets := sizes.Offsetsof(structFields(goStruct))
		total := sizes.Sizeof(goType)
		align := sizes.Alignof(goType)
		if total < 0 || total > math.MaxInt32 || align <= 0 || align > math.MaxInt32 {
			return refuse("layout of")
		}
		out := &goSourceUnsafeLayout{kind: 's', size: int(total), align: int(align), typ: typ}
		end := 0
		for i, field := range flat {
			layout, err := r.goSourceUnsafeByteLayout(field.typ, depth+1)
			if err != nil {
				return nil, err
			}
			offset := int(offsets[i])
			if offset > end || field.name == "_" || layout.padding {
				out.padding = true
			}
			if field.name == "_" || layout.blank {
				out.blank = true
			}
			if field.name != "_" {
				out.fields = append(out.fields, goSourceUnsafeLayoutField{name: field.name, offset: offset, layout: layout})
			}
			end = max(end, offset+layout.size)
		}
		if out.size > end {
			out.padding = true
		}
		return out, nil
	}
	return refuse("type")
}

// goSourceUnsafeAllocation is the canonical byte companion of one addressable
// interpreter cell. The declared value remains in the cell; this image keeps
// the otherwise unrepresented padding and exact pointer words. All refreshes,
// view writes, and typed publication use this one lock.
type goSourceUnsafeAllocation struct {
	mu     sync.Mutex
	image  *goSourceUnsafeImage
	source *goSourceUnsafeLayout
}

func (a *goSourceUnsafeAllocation) clone(pointer func(*bashPPPointer) *bashPPPointer) *goSourceUnsafeAllocation {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	out := &goSourceUnsafeAllocation{source: a.source}
	if a.image != nil {
		out.image = cloneGoSourceUnsafeImage(a.image, pointer)
	}
	return out
}

func cloneGoSourceUnsafeImage(in *goSourceUnsafeImage, pointer func(*bashPPPointer) *bashPPPointer) *goSourceUnsafeImage {
	if in == nil {
		return nil
	}
	out := &goSourceUnsafeImage{bytes: append([]byte(nil), in.bytes...)}
	if len(in.pointers) != 0 {
		out.pointers = make(map[int]*bashPPPointer, len(in.pointers))
		for offset, ptr := range in.pointers {
			if pointer != nil {
				ptr = pointer(ptr)
			}
			out.pointers[offset] = ptr
		}
	}
	return out
}

// goSourceUnsafeOverlayView identifies a byte range within an allocation.
// Field selection derives another view with a stable byte offset instead of
// appending a target-struct field name to the source struct's map path.
type goSourceUnsafeOverlayView struct {
	r      *Runner
	source *goSourceUnsafeLayout
	target *goSourceUnsafeLayout
	offset int
}

func (r *Runner) goSourceUnsafeStructOverlay(source, target syntax.BashPPTypeExpr) (*goSourceUnsafeOverlayView, error) {
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		return nil, fmt.Errorf("BASHPP-EUNSAFE-LAYOUT: struct overlays require a 64-bit little-endian gc layout, got %s", runtime.GOARCH)
	}
	if _, ok := r.bashPPUnderlyingType(source).(*syntax.BashPPStructType); !ok {
		return nil, goSourceUnsafeViewErr("source %s is not an interpreter-owned struct", bashPPTypeText(source))
	}
	if _, ok := r.bashPPUnderlyingType(target).(*syntax.BashPPStructType); !ok {
		return nil, goSourceUnsafeViewErr("target %s is not a struct overlay", bashPPTypeText(target))
	}
	from, err := r.goSourceUnsafeByteLayout(source, 0)
	if err != nil {
		return nil, err
	}
	to, err := r.goSourceUnsafeByteLayout(target, 0)
	if err != nil {
		return nil, err
	}
	if from.size != to.size || from.align != to.align {
		return nil, fmt.Errorf("BASHPP-EUNSAFE-LAYOUT: %s (%d bytes, align %d) and %s (%d bytes, align %d) cannot share a struct overlay", bashPPTypeText(source), from.size, from.align, bashPPTypeText(target), to.size, to.align)
	}
	if to.blank && len(to.fields) != 0 {
		return nil, goSourceUnsafeViewErr("target %s mixes observable fields with blank-field bytes that cannot be represented", bashPPTypeText(target))
	}
	return &goSourceUnsafeOverlayView{r: r, source: from, target: to}, nil
}

func (v *goSourceUnsafeOverlayView) allocation(ptr *bashPPPointer) (*goSourceUnsafeAllocation, error) {
	if ptr == nil || ptr.target == nil {
		return nil, errBashPPNilDereference
	}
	if len(ptr.path) != 0 {
		return nil, goSourceUnsafeViewErr("nested struct overlay storage is not yet representable")
	}
	ptr.target.lock()
	allocation := ptr.target.unsafeAllocation
	if allocation == nil {
		allocation = &goSourceUnsafeAllocation{source: v.source, image: &goSourceUnsafeImage{bytes: make([]byte, v.source.size)}}
		ptr.target.unsafeAllocation = allocation
	}
	ptr.target.unlock()
	if allocation.source.size != v.source.size || bashPPTypeText(allocation.source.typ) != bashPPTypeText(v.source.typ) {
		return nil, goSourceUnsafeViewErr("allocation byte image belongs to %s, not %s", bashPPTypeText(allocation.source.typ), bashPPTypeText(v.source.typ))
	}
	return allocation, nil
}

func (v *goSourceUnsafeOverlayView) refresh(ptr *bashPPPointer, allocation *goSourceUnsafeAllocation) (*goSourceUnsafeImage, error) {
	stored := ptr.target.view()
	value := stored.vr.Obj
	image := cloneGoSourceUnsafeImage(allocation.image, nil)
	if image == nil {
		image = &goSourceUnsafeImage{bytes: make([]byte, v.source.size)}
	}
	if err := v.r.goSourceUnsafeEncode(v.source, value, image, 0); err != nil {
		return nil, err
	}
	return image, nil
}

func (v *goSourceUnsafeOverlayView) read(ptr *bashPPPointer) (any, *bashPPCollectionMeta, error) {
	allocation, err := v.allocation(ptr)
	if err != nil {
		return nil, nil, err
	}
	allocation.mu.Lock()
	defer allocation.mu.Unlock()
	image, err := v.refresh(ptr, allocation)
	if err != nil {
		return nil, nil, err
	}
	value, meta, err := v.r.goSourceUnsafeDecode(v.target, image, v.offset)
	if err == nil {
		allocation.image = image
	}
	return value, meta, err
}

func (v *goSourceUnsafeOverlayView) write(ptr *bashPPPointer, value any, _ *bashPPCollectionMeta) error {
	allocation, err := v.allocation(ptr)
	if err != nil {
		return err
	}
	allocation.mu.Lock()
	defer allocation.mu.Unlock()
	image, err := v.refresh(ptr, allocation)
	if err != nil {
		return err
	}
	if err := v.r.goSourceUnsafeEncode(v.target, value, image, v.offset); err != nil {
		return err
	}
	decoded, meta, err := v.r.goSourceUnsafeDecode(v.source, image, 0)
	if err != nil {
		return err
	}
	allocation.image = image
	bashPPStoreCellValue(ptr.target, decoded, meta)
	return nil
}

func (v *goSourceUnsafeOverlayView) field(ptr *bashPPPointer, name string) (*bashPPPointer, error) {
	for _, field := range v.target.fields {
		if field.name != name {
			continue
		}
		out := ptr.clone()
		child := *v
		child.target = field.layout
		child.offset += field.offset
		out.setUnsafeOverlay(&child)
		out.elem = field.layout.typ
		return out, nil
	}
	return nil, goSourceUnsafeViewErr("field %s is not addressable in overlay %s", name, bashPPTypeText(v.target.typ))
}

// goSourceUnsafeImage is a run of bytes. A live pointer has no numeric
// address, so its word is carried beside the bytes, keyed by byte offset.
type goSourceUnsafeImage struct {
	bytes    []byte
	pointers map[int]*bashPPPointer
}

// pointerOverlap reports whether [offset, offset+size) touches the word of a
// live pointer. With exact set, a pointer word starting at offset itself is
// the one being read and does not count.
func (m *goSourceUnsafeImage) pointerOverlap(offset, size int, exact bool) bool {
	if len(m.pointers) == 0 {
		return false
	}
	for at := offset - 7; at < offset+size; at++ {
		if exact && at == offset {
			continue
		}
		if _, ok := m.pointers[at]; ok {
			return true
		}
	}
	return false
}

// goSourceUnsafeStoredInt reads an integer element in any of the carriers
// collection storage uses, as its 64-bit two's-complement pattern.
func goSourceUnsafeStoredInt(value any) (uint64, bool) {
	switch v := value.(type) {
	case nil:
		return 0, true
	case int:
		return uint64(v), true
	case int8:
		return uint64(v), true
	case int16:
		return uint64(v), true
	case int32:
		return uint64(v), true
	case int64:
		return uint64(v), true
	case uint:
		return uint64(v), true
	case uint8:
		return uint64(v), true
	case uint16:
		return uint64(v), true
	case uint32:
		return uint64(v), true
	case uint64:
		return v, true
	case uintptr:
		return uint64(v), true
	case string:
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return uint64(n), true
		}
		if n, err := strconv.ParseUint(v, 10, 64); err == nil {
			return n, true
		}
	}
	return 0, false
}

func goSourceUnsafeStoredFloat(value any) (float64, bool) {
	switch v := value.(type) {
	case nil:
		return 0, true
	case float64:
		return v, true
	case float32:
		return float64(v), true
	case int:
		return float64(v), true
	case string:
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f, true
		}
	}
	return 0, false
}

func (r *Runner) goSourceUnsafeEncode(layout *goSourceUnsafeLayout, value any, image *goSourceUnsafeImage, offset int) error {
	if moved, ok := value.(goSourceUnsafeMoved); ok {
		return moved
	}
	mismatch := func() error {
		return goSourceUnsafeViewErr("stored %T is not a representable %s value", value, bashPPTypeText(layout.typ))
	}
	out := image.bytes[offset : offset+layout.size]
	switch layout.kind {
	case 'b':
		switch v := value.(type) {
		case nil:
		case bool:
			if v {
				out[0] = 1
			}
		default:
			return mismatch()
		}
	case 'i':
		n, ok := goSourceUnsafeStoredInt(value)
		if !ok {
			return mismatch()
		}
		var word [8]byte
		binary.LittleEndian.PutUint64(word[:], n)
		copy(out, word[:])
	case 'f':
		f, ok := goSourceUnsafeStoredFloat(value)
		if !ok {
			return mismatch()
		}
		if layout.size == 4 {
			binary.LittleEndian.PutUint32(out, math.Float32bits(float32(f)))
		} else {
			binary.LittleEndian.PutUint64(out, math.Float64bits(f))
		}
	case 'p':
		ptr, ok := value.(*bashPPPointer)
		if value != nil && !ok {
			return mismatch()
		}
		if image.pointers != nil {
			delete(image.pointers, offset)
		}
		switch {
		case ptr == nil:
		case ptr.forged():
			binary.LittleEndian.PutUint64(out, ptr.unsafeAddress())
		default:
			if image.pointers == nil {
				image.pointers = make(map[int]*bashPPPointer)
			}
			image.pointers[offset] = ptr
		}
	case 's':
		if value == nil {
			return nil
		}
		mapping, ok := value.(map[string]any)
		if !ok {
			return mismatch()
		}
		for _, field := range layout.fields {
			item, _ := bashPPStorageGet(mapping, field.name)
			if err := r.goSourceUnsafeEncode(field.layout, item, image, offset+field.offset); err != nil {
				return err
			}
		}
	case 'a':
		if value == nil {
			return nil
		}
		seq, ok := value.([]any)
		if !ok || len(seq) != layout.count {
			return mismatch()
		}
		for i, item := range seq {
			if err := r.goSourceUnsafeEncode(layout.elem, item, image, offset+i*layout.elem.size); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *Runner) goSourceUnsafeDecode(layout *goSourceUnsafeLayout, image *goSourceUnsafeImage, offset int) (any, *bashPPCollectionMeta, error) {
	in := image.bytes[offset : offset+layout.size]
	if layout.kind != 's' && layout.kind != 'a' && image.pointerOverlap(offset, layout.size, layout.kind == 'p') {
		return nil, nil, goSourceUnsafeViewErr("the bytes of a live pointer cannot be read as %s", bashPPTypeText(layout.typ))
	}
	switch layout.kind {
	case 'b':
		if in[0] > 1 {
			return nil, nil, goSourceUnsafeViewErr("byte %d is not a %s value", in[0], bashPPTypeText(layout.typ))
		}
		return in[0] == 1, nil, nil
	case 'i':
		var word [8]byte
		copy(word[:], in)
		n := binary.LittleEndian.Uint64(word[:])
		if layout.signed {
			shift := uint(64 - 8*layout.size)
			return int(int64(n<<shift) >> shift), nil, nil
		}
		if n > math.MaxInt64 {
			// The carrier collection storage uses for a uint64 above the
			// int range (bashPPScalarAny).
			return strconv.FormatUint(n, 10), nil, nil
		}
		return int(n), nil, nil
	case 'f':
		var f float64
		if layout.size == 4 {
			f = float64(math.Float32frombits(binary.LittleEndian.Uint32(in)))
		} else {
			f = math.Float64frombits(binary.LittleEndian.Uint64(in))
		}
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, nil, goSourceUnsafeViewErr("a non-finite bit pattern cannot be read as %s", bashPPTypeText(layout.typ))
		}
		return f, nil, nil
	case 'p':
		meta := bashPPPointerMeta(layout.typ)
		if ptr := image.pointers[offset]; ptr != nil {
			return r.goSourceUnsafeRetypePointer(ptr, layout.typ), meta, nil
		}
		address := binary.LittleEndian.Uint64(in)
		if address == 0 {
			return nil, meta, nil
		}
		// Integer bytes read as a pointer are a forged address, as in
		// unsafe.Pointer(uintptr(n)): it compares and converts, and every
		// dereference refuses.
		forged := bashPPForgedPointer(address)
		if pointer, ok := r.bashPPPointerType(layout.typ); ok {
			forged.elem = pointer.Element
		}
		return forged, meta, nil
	case 's':
		value, meta := r.bashPPZeroValue(layout.typ)
		mapping, ok := value.(map[string]any)
		if !ok || meta == nil || meta.kind != "struct" {
			return nil, nil, goSourceUnsafeViewErr("%s has no struct storage", bashPPTypeText(layout.typ))
		}
		for _, field := range layout.fields {
			item, child, err := r.goSourceUnsafeDecode(field.layout, image, offset+field.offset)
			if err != nil {
				return nil, nil, err
			}
			bashPPStorageSetField(mapping, meta.mapping, field.name, item, child)
		}
		return mapping, meta, nil
	case 'a':
		seq := make([]any, layout.count)
		meta := &bashPPCollectionMeta{kind: "array", typ: layout.typ, sequence: make([]*bashPPCollectionMeta, layout.count)}
		for i := range seq {
			var err error
			if seq[i], meta.sequence[i], err = r.goSourceUnsafeDecode(layout.elem, image, offset+i*layout.elem.size); err != nil {
				return nil, nil, err
			}
		}
		return seq, meta, nil
	}
	return nil, nil, goSourceUnsafeViewErr("%s has no byte layout", bashPPTypeText(layout.typ))
}

// goSourceUnsafeRetypePointer is `(T)(unsafe.Pointer(p))` for a pointer word
// read through a view: the same storage, presented as the view's pointer type
// under the rules bashPPPointerConversion applies to a spelled conversion.
func (r *Runner) goSourceUnsafeRetypePointer(ptr *bashPPPointer, typ syntax.BashPPTypeExpr) *bashPPPointer {
	retyped := *ptr
	retyped.path = append([]bashPPPointerStep(nil), ptr.path...)
	if retyped.unsafeSource() == nil {
		retyped.setUnsafeSource(ptr.elem)
	}
	pointer, ok := r.bashPPPointerType(typ)
	if !ok || pointer.Element == nil {
		// unsafe.Pointer: the storage type travels in unsafeSource.
		return &retyped
	}
	retyped.cold = nil
	if bashPPTypeText(retyped.unsafeSource()) != bashPPTypeText(pointer.Element) {
		if view := r.goSourceUnsafeSliceHeaderView(retyped.unsafeSource(), pointer.Element); view != nil {
			retyped.setUnsafeSlice(view)
		} else if overlay, overlayErr := r.goSourceUnsafeStructOverlay(retyped.unsafeSource(), pointer.Element); overlayErr == nil {
			retyped.setUnsafeOverlay(overlay)
		} else {
			retyped.setUnsafeRefusal(overlayErr)
		}
	}
	retyped.elem = pointer.Element
	return &retyped
}
