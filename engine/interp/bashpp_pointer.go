// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"errors"
	"fmt"
	"go/constant"
	"strings"
	"sync"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

type bashPPPointerStep struct {
	field string
	index int
	deref bool
}

// bashPPPointer names storage, not a copied value. Keeping the root cell makes
// aliases and closure captures exact; the scope cloner rewrites that edge at
// every process/task snapshot boundary.
type bashPPPointer struct {
	target *bashPPCell
	path   []bashPPPointerStep
	elem   syntax.BashPPTypeExpr
	// storageAddress is the runtime address shared by standalone zero-sized
	// objects. It is separate from target: each object still has its own typed
	// value cell, while its address follows Go's zerobase allocation policy.
	// Subobjects of nonzero containers leave this nil and retain target+path.
	storageAddress *byte
	// cold carries the unsafe-conversion state that almost no pointer has. It
	// is immutable once published: pointer copies share it and every setter
	// replaces it, so a retyped copy never writes through to its source.
	cold *bashPPPointerCold
}

// bashPPPointerCold is the rarely used part of a pointer, kept out of line so
// the common object pointer stays small.
type bashPPPointerCold struct {
	// unsafeSource records the concrete storage type carried through an
	// unsafe.Pointer conversion. unsafeView is the validated blank-only
	// struct view presented by a following typed-pointer conversion.
	unsafeSource syntax.BashPPTypeExpr
	unsafeView   syntax.BashPPTypeExpr
	// unsafeOverlay is a byte-addressed view of the allocation. Unlike
	// unsafeView's historical blank-only zero value, it persists padding and
	// decodes observable fields on every access.
	unsafeOverlay *goSourceUnsafeOverlayView
	// unsafeRefusal is the validation failure of a typed-pointer conversion
	// of an unsafe.Pointer whose target is no supported view: the pointer
	// exists, but no access through it can be represented.
	unsafeRefusal error
	// unsafeSlice is set instead when the conversion reads a slice-header
	// struct as a slice; see gosource_unsafe_slice_view.go.
	unsafeSlice *goSourceUnsafeSliceView
	// unsafeOffset is a residual byte offset left by unsafe.Add outside the
	// element grid of the span; forged/unsafeAddress name an opaque address
	// converted from an integer. Neither may be dereferenced; see
	// bashpp_s247_unsafe.go.
	unsafeOffset  int64
	forged        bool
	unsafeAddress uint64
}

func (p *bashPPPointer) edit(f func(*bashPPPointerCold)) {
	var next bashPPPointerCold
	if p.cold != nil {
		next = *p.cold
	}
	f(&next)
	if next.empty() {
		p.cold = nil
		return
	}
	p.cold = &next
}

func (c *bashPPPointerCold) empty() bool {
	return c.unsafeSource == nil && c.unsafeView == nil && c.unsafeOverlay == nil && c.unsafeRefusal == nil &&
		c.unsafeSlice == nil && c.unsafeOffset == 0 && !c.forged && c.unsafeAddress == 0
}

func bashPPForgedPointer(address uint64) *bashPPPointer {
	return &bashPPPointer{cold: &bashPPPointerCold{forged: true, unsafeAddress: address}}
}

func (p *bashPPPointer) unsafeSource() syntax.BashPPTypeExpr {
	if p.cold == nil {
		return nil
	}
	return p.cold.unsafeSource
}

func (p *bashPPPointer) unsafeView() syntax.BashPPTypeExpr {
	if p.cold == nil {
		return nil
	}
	return p.cold.unsafeView
}

func (p *bashPPPointer) unsafeOverlay() *goSourceUnsafeOverlayView {
	if p.cold == nil {
		return nil
	}
	return p.cold.unsafeOverlay
}

func (p *bashPPPointer) unsafeRefusal() error {
	if p.cold == nil {
		return nil
	}
	return p.cold.unsafeRefusal
}

func (p *bashPPPointer) unsafeSlice() *goSourceUnsafeSliceView {
	if p.cold == nil {
		return nil
	}
	return p.cold.unsafeSlice
}

func (p *bashPPPointer) unsafeOffset() int64 {
	if p.cold == nil {
		return 0
	}
	return p.cold.unsafeOffset
}

func (p *bashPPPointer) forged() bool { return p.cold != nil && p.cold.forged }

func (p *bashPPPointer) unsafeAddress() uint64 {
	if p.cold == nil {
		return 0
	}
	return p.cold.unsafeAddress
}

func (p *bashPPPointer) setUnsafeSource(v syntax.BashPPTypeExpr) {
	p.edit(func(c *bashPPPointerCold) { c.unsafeSource = v })
}

func (p *bashPPPointer) setUnsafeView(v syntax.BashPPTypeExpr) {
	p.edit(func(c *bashPPPointerCold) { c.unsafeView = v })
}

func (p *bashPPPointer) setUnsafeOverlay(v *goSourceUnsafeOverlayView) {
	p.edit(func(c *bashPPPointerCold) { c.unsafeOverlay = v })
}

func (p *bashPPPointer) setUnsafeRefusal(v error) {
	p.edit(func(c *bashPPPointerCold) { c.unsafeRefusal = v })
}

func (p *bashPPPointer) setUnsafeSlice(v *goSourceUnsafeSliceView) {
	p.edit(func(c *bashPPPointerCold) { c.unsafeSlice = v })
}

func (p *bashPPPointer) setUnsafeOffset(v int64) {
	p.edit(func(c *bashPPPointerCold) { c.unsafeOffset = v })
}

func (p *bashPPPointer) setUnsafeAddress(v uint64) {
	p.edit(func(c *bashPPPointerCold) { c.unsafeAddress = v })
}

func (p *bashPPPointer) clone() *bashPPPointer {
	if p == nil {
		return nil
	}
	out := *p
	out.path = append([]bashPPPointerStep(nil), p.path...)
	return &out
}

// bashPPZeroSizeStorage is real process storage used as the canonical address
// for standalone zero-sized Go-source objects. The byte is deliberately
// nonzero-sized: its Go address is stable and never relies on the compiler's
// own optional coalescing of zero-sized host variables.
var bashPPZeroSizeStorage byte

func (r *Runner) bashPPPointerForStorage(target *bashPPCell, elem syntax.BashPPTypeExpr, standalone bool) *bashPPPointer {
	ptr := &bashPPPointer{target: target, elem: elem}
	if standalone && r.bashPPGoSource {
		if r.bashPPZeroSizeElem(elem) {
			ptr.storageAddress = &bashPPZeroSizeStorage
		}
	}
	return ptr
}

// bashPPZeroSizeElem reports whether a Go object of type elem occupies no
// storage. Building the reflect shape of a struct is costly and runs for every
// pointer taken, so a type with a field that certainly has storage is answered
// from its declaration; every other type keeps the reflect shape as authority.
func (r *Runner) bashPPZeroSizeElem(elem syntax.BashPPTypeExpr) bool {
	if r.bashPPKnownSized(elem, 0) {
		return false
	}
	shape, ok := r.bashPPGoShapeType(elem, 0)
	return ok && shape.Size() == 0
}

// bashPPKnownSized reports a type that certainly occupies storage. A false
// answer means only "not established".
func (r *Runner) bashPPKnownSized(typ syntax.BashPPTypeExpr, depth int) bool {
	if typ == nil || depth > 8 {
		return false
	}
	if named, ok := typ.(*syntax.BashPPNamedType); ok && named.Name != nil {
		if _, declared := r.bashPPTypes[named.Name.Value]; !declared && bashPPPredeclaredSizedName(named.Name.Value) {
			return true
		}
	}
	if _, ok := r.bashPPInterfaceType(typ); ok {
		return true
	}
	switch x := r.bashPPUnderlyingType(typ).(type) {
	case *syntax.BashPPNamedType:
		return x.Name != nil && bashPPPredeclaredSizedName(x.Name.Value)
	case *syntax.BashPPPointerType, *syntax.BashPPChanType, *syntax.BashPPFuncType:
		return true
	case *syntax.BashPPCollectionType:
		return x.Kind == "map" || x.Kind == "slice"
	case *syntax.BashPPStructType:
		for _, field := range x.Fields {
			if r.bashPPKnownSized(field.FieldTypeExpr, depth+1) {
				return true
			}
		}
	}
	return false
}

func bashPPPredeclaredSizedName(name string) bool {
	switch name {
	case "bool", "string", "int8", "uint8", "byte", "int16", "uint16", "int32", "uint32", "rune",
		"int64", "uint64", "int", "uint", "uintptr", "float32", "float64", "complex64", "complex128":
		return true
	}
	return false
}

// bashPPPointerMetas shares the layout record of pointer slots by declared type:
// a pointer's metadata names only its type, so a slice of a million *T would
// otherwise carry a million identical copies. Callers never write to a pointer
// record (every mutation site operates on a fresh or copied record). The table
// is bounded because types synthesized at run time have no shared owner.
var bashPPPointerMetas struct {
	mu    sync.RWMutex
	byTyp map[syntax.BashPPTypeExpr]*bashPPCollectionMeta
}

const bashPPPointerMetaLimit = 4096

func bashPPPointerMeta(typ syntax.BashPPTypeExpr) *bashPPCollectionMeta {
	if typ == nil {
		return &bashPPCollectionMeta{kind: "pointer"}
	}
	t := &bashPPPointerMetas
	t.mu.RLock()
	meta := t.byTyp[typ]
	t.mu.RUnlock()
	if meta != nil {
		return meta
	}
	meta = &bashPPCollectionMeta{kind: "pointer", typ: typ}
	t.mu.Lock()
	if shared := t.byTyp[typ]; shared != nil {
		meta = shared
	} else if len(t.byTyp) < bashPPPointerMetaLimit {
		if t.byTyp == nil {
			t.byTyp = make(map[syntax.BashPPTypeExpr]*bashPPCollectionMeta)
		}
		t.byTyp[typ] = meta
	}
	t.mu.Unlock()
	return meta
}

func bashPPScalarKindForType(typ syntax.BashPPTypeExpr) constant.Kind {
	switch bashPPTypeText(typ) {
	case "string":
		return constant.String
	case "bool":
		return constant.Bool
	case "int", "int8", "int16", "int32", "int64", "uint", "uint8", "uint16", "uint32", "uint64", "uintptr", "byte", "rune":
		return constant.Int
	case "float32", "float64":
		return constant.Float
	case "complex64", "complex128":
		return constant.Complex
	}
	return constant.Unknown
}

func bashPPCanonicalPointerAddress(ptr *bashPPPointer, elem syntax.BashPPTypeExpr) (*bashPPPointer, *bashPPCollectionMeta, error) {
	if ptr == nil {
		return nil, nil, errBashPPNilDereference
	}
	value, _, _, err := ptr.read()
	if err != nil {
		return nil, nil, err
	}
	if value == nil {
		return nil, nil, errBashPPNilDereference
	}
	base, ok := value.(*bashPPPointer)
	if !ok {
		return nil, nil, fmt.Errorf("BASHPP-EPOINTER-TARGET: dereference result is not a pointer")
	}
	if base == nil {
		return nil, nil, errBashPPNilDereference
	}
	out := base.clone()
	out.elem = elem
	_, meta, _, err := out.read()
	if err != nil {
		return nil, nil, err
	}
	return out, meta, nil
}

func (r *Runner) bashPPPointerType(typ syntax.BashPPTypeExpr) (*syntax.BashPPPointerType, bool) {
	if pointer, ok := typ.(*syntax.BashPPPointerType); ok {
		return pointer, true
	}
	shape := r.bashPPUnderlyingType(typ)
	pointer, ok := shape.(*syntax.BashPPPointerType)
	return pointer, ok
}

func (r *Runner) bashPPPointerExprType(expr syntax.BashPPExpr, ptr *bashPPPointer) syntax.BashPPTypeExpr {
	if target, nilConversion := r.bashPPNilPointerConversion(expr); nilConversion {
		return target
	}
	switch x := expr.(type) {
	case *syntax.BashPPParenExpr:
		return r.bashPPPointerExprType(x.X, ptr)
	case *syntax.BashPPIdent:
		if cell := r.bashPPScope.lookup(x.Name.Value).view(); cell != nil && cell.pointer {
			return cell.declType
		}
	case *syntax.BashPPSelectorExpr, *syntax.BashPPIndexExpr, *syntax.BashPPSliceExpr:
		_, meta, err := r.bashPPReadExpr(expr)
		if err == nil && meta != nil && meta.kind == "pointer" {
			return meta.typ
		}
	case *syntax.BashPPDerefExpr:
		outer, err := r.bashPPPointerExprValue(x.X)
		if err == nil && outer != nil {
			_, meta, _, readErr := outer.read()
			if readErr == nil && meta != nil && meta.kind == "pointer" {
				return meta.typ
			}
		}
	}
	if ptr == nil {
		return nil
	}
	return &syntax.BashPPPointerType{Element: ptr.elem}
}

func (r *Runner) bashPPValidatePointerType(typ syntax.BashPPTypeExpr) error {
	ptr, ok := r.bashPPPointerType(typ)
	if !ok || ptr.Element == nil {
		return fmt.Errorf("BASHPP-EPOINTER-TYPE: invalid pointer type %s", bashPPTypeText(typ))
	}
	if err := r.bashPPValidateValueType(ptr.Element, make(map[string]bool)); err != nil {
		return fmt.Errorf("BASHPP-EPOINTER-TYPE: %v", err)
	}
	return nil
}

// bashPPPointerConversion settles `(*T)(p)` — a conversion whose target is
// a pointer type — to the same pointer retyped: it names the storage p
// names, and dereferences and method selection read it as a T. It reports
// whether the expression was such a conversion; a nil operand stays nil.
// The type reported is the target as spelled: a conversion to a defined
// pointer type (`Token(s)` for `type Token *[16]byte`) or to a type
// parameter bound to one is a value of that type, not of its underlying
// pointer shape, so it assigns to a variable declared with the same name.
func (r *Runner) bashPPPointerConversion(expr syntax.BashPPExpr) (*bashPPPointer, syntax.BashPPTypeExpr, bool, error) {
	for {
		paren, ok := expr.(*syntax.BashPPParenExpr)
		if !ok {
			break
		}
		expr = paren.X
	}
	conv, ok := expr.(*syntax.BashPPConvertExpr)
	if !ok {
		return nil, nil, false, nil
	}
	spelled := r.bashPPConvertTarget(conv)
	target, ok := r.bashPPPointerType(spelled)
	if !ok || target.Element == nil {
		return nil, nil, false, nil
	}
	if goSourceNilLiteral(conv.X) {
		return nil, spelled, true, nil
	}
	if ptr, converted, err := r.bashPPSliceToArrayPointer(conv, target); converted {
		return ptr, spelled, true, err
	}
	ptr, err := r.bashPPPointerExprValue(conv.X)
	if err != nil || ptr == nil {
		return nil, spelled, true, err
	}
	retyped := *ptr
	retyped.setUnsafeRefusal(nil)
	retyped.setUnsafeSlice(nil)
	retyped.setUnsafeOverlay(nil)
	if ptr.unsafeSource() != nil && bashPPTypeText(ptr.unsafeSource()) != bashPPTypeText(target.Element) {
		if view := r.goSourceUnsafeSliceHeaderView(ptr.unsafeSource(), target.Element); view != nil {
			retyped.setUnsafeView(nil)
			retyped.setUnsafeSlice(view)
		} else if overlay, overlayErr := r.goSourceUnsafeStructOverlay(ptr.unsafeSource(), target.Element); overlayErr == nil {
			retyped.setUnsafeView(nil)
			retyped.setUnsafeOverlay(overlay)
		} else if r.bashPPGoSource {
			// Go permits the conversion itself; only an access through the
			// result reinterprets bytes. The retyped pointer still names the
			// source storage (so it is compared, stored and converted back
			// exactly as Go does), and every read or write through it is
			// refused with this validation's diagnostic.
			retyped.setUnsafeView(nil)
			retyped.setUnsafeRefusal(overlayErr)
		} else {
			return nil, target, true, overlayErr
		}
	}
	retyped.elem = target.Element
	return &retyped, spelled, true, nil
}

// bashPPSliceToArrayPointer applies Go's `(*[N]T)(s)` conversion of a slice
// to a pointer to its underlying array: the result aliases the slice's
// storage (a write through it is a write to the slice), a nil slice converts
// to a nil pointer, and a slice shorter than N raises Go's runtime error
// rather than a refusal. It reports whether the conversion was such a shape;
// a pointer operand is left to the retyping path, since `(*T)(p)` keeps its
// own meaning when T is an array type.
func (r *Runner) bashPPSliceToArrayPointer(conv *syntax.BashPPConvertExpr, target *syntax.BashPPPointerType) (*bashPPPointer, bool, error) {
	array, ok := r.bashPPUnderlyingType(target.Element).(*syntax.BashPPCollectionType)
	if !ok || array.Kind != "array" || array.Length == nil || r.bashPPScope == nil {
		return nil, false, nil
	}
	operand := conv.X
	for {
		paren, ok := operand.(*syntax.BashPPParenExpr)
		if !ok {
			break
		}
		operand = paren.X
	}
	// Keep non-collection call and pointer-conversion operands on their existing path.
	root, rooted := bashPPCollectionRoot(operand)
	if !rooted || r.bashPPScope.lookup(root) == nil {
		return nil, false, nil
	}
	if ident, direct := operand.(*syntax.BashPPIdent); direct {
		cell := r.bashPPScope.lookup(ident.Name.Value).view()
		if cell != nil && cell.pointer {
			return nil, false, nil
		}
	}
	// Evaluate the complete slice operand once before constructing the pointer.
	// Storing that value in an anonymous cell captures its slice header: nested
	// views keep their own length and capacity, while the []any payload still
	// shares the original backing array. In particular, a bound expression or a
	// later statement may reassign the source variable without redirecting this
	// pointer to the replacement slice.
	value, meta, err := r.bashPPReadExpr(operand)
	if err != nil {
		return nil, true, err
	}
	if meta == nil || meta.kind != "slice" {
		return nil, false, nil
	}
	cell := &bashPPCell{declType: meta.typ}
	bashPPStoreCellValue(cell, value, meta)
	ptr := &bashPPPointer{target: cell}
	n, err := r.bashPPArrayLength(array.Length.Value)
	if err != nil {
		return nil, true, fmt.Errorf("BASHPP-EPOINTER-TYPE: %v", err)
	}
	seq, _ := value.([]any)
	if seq == nil {
		return nil, true, nil
	}
	length := len(seq)
	if length < n {
		message := fmt.Sprintf("runtime error: cannot convert slice with length %d to array or pointer to array with length %d", length, n)
		if r.bashPPGoSource {
			return nil, true, r.bashPPRaiseRuntimeError("runtime.errorString", message)
		}
		return nil, true, fmt.Errorf("BASHPP-ECOLLECTION-BOUNDS: %s", message)
	}
	ptr.elem = target.Element
	return ptr, true, nil
}

func (r *Runner) bashPPPointerExprValue(expr syntax.BashPPExpr) (ptr *bashPPPointer, err error) {
	defer func() { err = r.goSourceRuntimeFaultAt(err, expr) }()
	if _, nilConversion := r.bashPPNilPointerConversion(expr); nilConversion {
		return nil, nil
	}
	if ptr, _, handled, err := r.goSourceUnsafePointerExpr(expr); handled {
		return ptr, err
	}
	if ptr, handled, err := r.goSourceUnsafeDataPointer(expr); handled {
		return ptr, err
	}
	if ptr, _, converted, err := r.bashPPPointerConversion(expr); converted {
		return ptr, err
	}
	switch x := expr.(type) {
	case *syntax.BashPPParenExpr:
		return r.bashPPPointerExprValue(x.X)
	case *syntax.BashPPConvertExpr:
		if r.bashPPGoSource && r.goSourceUnsafePointerType(r.bashPPConvertTarget(x)) {
			ptr, err := r.bashPPPointerExprValue(x.X)
			if err != nil && strings.HasPrefix(err.Error(), "BASHPP-EPOINTER-TARGET:") {
				// The operand names no interpreter storage — typically an
				// integer spelled as an address. Integers are never trusted
				// as addresses (the constant 0 lowers to nil beforehand).
				return nil, fmt.Errorf("BASHPP-EUNSAFE-ADDRESS: unsafe.Pointer operand is not an interpreter-owned live pointer (%v)", err)
			}
			if err != nil || ptr == nil {
				return ptr, err
			}
			view := *ptr
			if view.unsafeSource() == nil {
				view.setUnsafeSource(ptr.elem)
			}
			return &view, nil
		}
		return nil, fmt.Errorf("BASHPP-EPOINTER-TARGET: expression is not a pointer")
	case *syntax.BashPPAddressExpr:
		return r.bashPPAddress(x.X)
	case *syntax.BashPPNewExpr:
		// Inside a generic body `new(T)` allocates the frame's type
		// argument: the pointer's dynamic type is *E, never the parameter
		// spelling *T, which has no method set outside this frame.
		alloc := r.bashPPBindTypeExpr(x.AllocType)
		if err := r.bashPPValidateValueType(alloc, make(map[string]bool)); err != nil {
			return nil, fmt.Errorf("BASHPP-EPOINTER-TYPE: %v", err)
		}
		value, meta := r.bashPPZeroValue(alloc)
		if x.Init != nil {
			initialized, initializedMeta, err := r.bashPPEvalTypedValue(x.Init, alloc)
			if err != nil {
				return nil, err
			}
			value, meta = initialized, initializedMeta
		}
		cell := &bashPPCell{declType: alloc}
		if vr, ok := r.bashPPGoSourceCollectionCarrier(value, meta); ok {
			cell.vr, cell.valueMeta = vr, meta
			cell.object = &bashPPObjectIdentity{collection: meta}
		} else {
			bashPPStoreCellValue(cell, value, meta)
		}
		return r.bashPPPointerForStorage(cell, alloc, true), nil
	case *syntax.BashPPIdent:
		cell := r.bashPPScope.lookup(x.Name.Value).view()
		if cell == nil || !cell.pointer {
			if native := r.goSourceNativePointerCell(x.Name.Value); native != nil {
				return nil, &bashPPNativePointerValueError{value: native}
			}
			return nil, fmt.Errorf("BASHPP-EPOINTER-TARGET: %s is not a pointer", x.Name.Value)
		}
		if cell.pointerValue == nil {
			return nil, nil
		}
		return cell.pointerValue, nil
	case *syntax.BashPPDerefExpr:
		outer, err := r.bashPPPointerExprValue(x.X)
		if err != nil {
			return nil, err
		}
		if outer == nil {
			return nil, errBashPPNilDereference
		}
		value, _, _, err := outer.read()
		if err != nil {
			return nil, err
		}
		ptr, ok := value.(*bashPPPointer)
		if !ok {
			return nil, fmt.Errorf("BASHPP-EPOINTER-TARGET: dereference result is not a pointer")
		}
		return ptr, nil
	default:
		value, _, err := r.bashPPReadExpr(expr)
		if err != nil {
			return nil, err
		}
		ptr, ok := value.(*bashPPPointer)
		if !ok && value != nil {
			if native, isNative := value.(*bashPPBridgeValue); isNative {
				return nil, &bashPPNativePointerValueError{value: native}
			}
			return nil, fmt.Errorf("BASHPP-EPOINTER-TARGET: expression is not a pointer")
		}
		return ptr, nil
	}
}

// bashPPNativePointerValueError reports a pointer-typed expression whose
// value is a dependency's own pointer — an authenticated native handle, not
// an interpreter pointer. `var recv = parse(...)` with parse returning
// *template.Template evaluates to such a handle. Callers that can store the
// handle recover it from the error; everywhere else it reads as the same
// pointer-target refusal as before.
type bashPPNativePointerValueError struct{ value *bashPPBridgeValue }

func (e *bashPPNativePointerValueError) Error() string {
	return "BASHPP-EPOINTER-TARGET: expression is not a pointer"
}

func (r *Runner) bashPPAddress(expr syntax.BashPPExpr) (result *bashPPPointer, err error) {
	defer func() { err = r.goSourceRuntimeFaultAt(err, expr) }()
	for {
		paren, ok := expr.(*syntax.BashPPParenExpr)
		if !ok {
			break
		}
		expr = paren.X
	}
	// The literal's imported target type establishes dependency ownership;
	// zero values, nil fields and other interpreter-evaluated field expressions
	// do not turn it into local struct storage. Keep the authenticated native
	// pointer carrier used by every other imported composite boundary.
	if lit, ok := expr.(*syntax.BashPPCompositeLit); ok && r.bashPPNativeType(lit.LitType) {
		native, err := r.bashPPNativeComposite(lit, true)
		if err != nil {
			return nil, err
		}
		return nil, &bashPPNativePointerValueError{value: &native}
	}
	if r.goSourceNativeAddressable(expr) {
		native, err := r.goSourceNativeAddress(expr)
		if err != nil {
			return nil, err
		}
		return nil, &bashPPNativePointerValueError{value: &native}
	}
	if deref, ok := expr.(*syntax.BashPPDerefExpr); ok {
		ptr, err := r.bashPPPointerExprValue(deref.X)
		if err == nil && ptr == nil {
			err = errBashPPNilDereference
		}
		return ptr, err
	}
	// Go's `&T{…}` is addressable even though it names no variable: the
	// literal is a fresh allocation whose address the expression yields. It
	// gets an anonymous cell to live in, exactly as `new(T)` does above.
	if lit, ok := expr.(*syntax.BashPPCompositeLit); ok {
		return r.bashPPCompositeAddress(lit)
	}
	// A map element itself is never addressable, but a pointer stored in a map
	// still points at addressable storage. Go therefore permits m[k].field when
	// m's element type is *T. Resolve that pointer value once and continue the
	// address path from its pointee; do not try to take the map element's
	// address. Keeping this case here also leaves m[k].field rejected when the
	// element is a non-pointer struct.
	if selector, ok := expr.(*syntax.BashPPSelectorExpr); ok && r.bashPPGoSource {
		if index, indexed := selector.X.(*syntax.BashPPIndexExpr); indexed {
			indexedType, typed := r.goSourceStaticExprType(index)
			if _, pointerElement := r.bashPPUnderlyingType(indexedType).(*syntax.BashPPPointerType); !typed || !pointerElement {
				goto ordinaryAddress
			}
			parent, _, readErr := r.bashPPReadExpr(index)
			if readErr != nil {
				return nil, readErr
			}
			if base, pointerElement := parent.(*bashPPPointer); pointerElement {
				return r.goSourcePointeeFieldAddress(base, selector.Sel.Value)
			}
			// The index has already run. A missing or unsupported pointer
			// carrier must not fall through and evaluate it a second time.
			if parent == nil {
				return nil, errBashPPNilDereference
			}
			return nil, fmt.Errorf("BASHPP-EPOINTER-TARGET: indexed value has no interpreter pointer storage")
		}
	}

	// x.(*T).f is addressable through the asserted pointer: the assertion
	// runs once and the field address continues from its pointee.
	if selector, ok := expr.(*syntax.BashPPSelectorExpr); ok && r.bashPPGoSource {
		if base, _, converted, err := r.bashPPPointerConversion(selector.X); converted {
			if err != nil {
				return nil, err
			}
			return r.goSourcePointeeFieldAddress(base, selector.Sel.Value)
		}
		if parentType, known := r.goSourceStaticExprType(selector.X); known {
			if _, pointer := r.bashPPPointerType(parentType); pointer {
				base, err := r.bashPPPointerExprValue(selector.X)
				if err != nil {
					return nil, err
				}
				return r.goSourcePointeeFieldAddress(base, selector.Sel.Value)
			}
		}
		x := selector.X
		for {
			paren, parenthesised := x.(*syntax.BashPPParenExpr)
			if !parenthesised {
				break
			}
			x = paren.X
		}
		// f().f likewise: the call runs once and the field address continues
		// from the pointee of its pointer result.
		if call, called := x.(*syntax.BashPPCall); called && r.goSourcePointerCallResult(call) {
			base, err := r.bashPPPointerExprValue(call)
			if err != nil {
				return nil, err
			}
			return r.goSourcePointeeFieldAddress(base, selector.Sel.Value)
		}
		if assert, asserted := x.(*syntax.BashPPTypeAssertExpr); asserted && assert.TypeToken == nil {
			if _, pointer := r.bashPPPointerType(assert.Assert); pointer {
				base, err := r.bashPPPointerExprValue(assert)
				if err != nil {
					return nil, err
				}
				return r.goSourcePointeeFieldAddress(base, selector.Sel.Value)
			}
		}
	}

ordinaryAddress:
	// f().field[i] names no variable, but the collection it indexes lives
	// behind the pointer the call returned. Resolve that field's address by
	// the selector rules above, which run the call once, and continue the
	// path from there to the element.
	if index, indexed := expr.(*syntax.BashPPIndexExpr); indexed && r.bashPPGoSource {
		if _, rooted := bashPPCollectionRoot(expr); !rooted {
			if selector, selected := index.X.(*syntax.BashPPSelectorExpr); selected {
				base, err := r.bashPPAddress(selector)
				if err != nil {
					return nil, err
				}
				collection, found := r.bashPPUnderlyingType(base.elem).(*syntax.BashPPCollectionType)
				if found && (collection.Kind == "slice" || collection.Kind == "array") {
					at, err := r.bashPPCollectionIndex(index.Index)
					if err != nil {
						return nil, err
					}
					value, _, _, err := base.read()
					if err != nil {
						return nil, err
					}
					seq, _ := value.([]any)
					if at.outOfBounds(len(seq)) {
						return nil, r.bashPPSprint162CollectionBoundsPanic(index, at, len(seq))
					}
					path := append(append([]bashPPPointerStep(nil), base.path...), bashPPPointerStep{index: at.value})
					return &bashPPPointer{target: base.target, path: path, elem: collection.Element}, nil
				}
			}
		}
	}
	root, ok := bashPPCollectionRoot(expr)
	if !ok || r.bashPPScope == nil {
		return nil, fmt.Errorf("%sBASHPP-ENONADDRESSABLE: operand is not addressable (%T)", r.bashErrPrefix(expr.Pos()), expr)
	}
	cell := r.bashPPScope.lookup(root)
	if cell == nil {
		return nil, fmt.Errorf("BASHPP-EPOINTER-TARGET: undefined pointer target %s", root)
	}
	// The pointer must name the REAL storage, so cell itself is what goes into
	// target; every field this decision reads comes from one snapshot of it.
	stored := cell.view()
	if stored.constant {
		return nil, fmt.Errorf("BASHPP-ENONADDRESSABLE: constant %s is not addressable", root)
	}
	ptr := &bashPPPointer{target: cell}
	typ := stored.declType
	// Taking a typed variable's address needs its storage and declared type,
	// not its current value metadata. Atomic operations may be updating that
	// metadata concurrently before this address reaches the atomic lock.
	if _, direct := expr.(*syntax.BashPPIdent); direct && typ != nil {
		ptr.elem = typ
		if r.bashPPGoSource && r.bashPPZeroSizeElem(typ) {
			ptr.storageAddress = &bashPPZeroSizeStorage
		}
		return ptr, nil
	}
	if _, direct := expr.(*syntax.BashPPIdent); direct && typ == nil && stored.typeName == "" {
		// A short declaration initialized by a function literal stores the
		// closure handle as text, but its address still has the inferred func
		// type. Preserve that type on the pointer just as an explicit `var f
		// func()` declaration does.
		if functionType := r.bashPPFuncValueType(stored); functionType != nil {
			ptr.elem = functionType
			return ptr, nil
		}
	}
	meta := bashPPCellMeta(cell)
	if stored.pointer {
		if _, direct := expr.(*syntax.BashPPIdent); !direct {
			if stored.pointerValue == nil {
				return nil, errBashPPNilDereference
			}
			base := stored.pointerValue
			ptr = base.clone()
			typ = base.elem
			_, meta, _, _ = base.read()
		}
	}
	if typ == nil && meta != nil {
		typ = meta.typ
	}
	if typ == nil {
		typ = bashPPInferredCellType(stored)
	}
	var descend func(syntax.BashPPExpr) error
	descend = func(node syntax.BashPPExpr) error {
		switch x := node.(type) {
		case *syntax.BashPPIdent:
			return nil
		case *syntax.BashPPParenExpr:
			return descend(x.X)
		case *syntax.BashPPDerefExpr:
			// The setup above already followed the root pointer once, so the
			// dereference naming that root contributes no further step -- this
			// is how `(*p)[i]` reaches the same address as `p[i]` would if Go
			// allowed that spelling. A deeper dereference would need a step
			// this walker does not build, so it is refused rather than
			// silently resolved to the wrong address.
			inner, isIdent := x.X.(*syntax.BashPPIdent)
			if isIdent && stored.pointer && inner.Name.Value == root {
				return nil
			}
			return fmt.Errorf("%sBASHPP-ENONADDRESSABLE: operand is not addressable (%T)", r.bashErrPrefix(x.Pos()), x)
		case *syntax.BashPPSelectorExpr:
			if err := descend(x.X); err != nil {
				return err
			}
			// Go's selector dereferences a pointer base implicitly, so
			// (*pn).left and s.Subp.SubpSub select on the pointee.
			// bashPPResolveField follows that pointer statically; the
			// runtime path must record the indirection too, or the stored
			// path later reads the pointer value where struct storage is
			// expected.
			if pointer, isPointer := typ.(*syntax.BashPPPointerType); isPointer {
				if r.bashPPGoSource {
					canonical, derefMeta, err := bashPPCanonicalPointerAddress(ptr, pointer.Element)
					if err != nil {
						return err
					}
					ptr, meta = canonical, derefMeta
				} else {
					ptr.path = append(ptr.path, bashPPPointerStep{deref: true})
					meta = nil
				}
				typ = pointer.Element
			}
			sel := r.bashPPResolveField(typ, x.Sel.Value)
			if sel.ambiguous || len(sel.edges) == 0 {
				return bashPPSelectionError(typ, x.Sel.Value, sel)
			}
			for i, edge := range sel.edges {
				ptr.path = append(ptr.path, bashPPPointerStep{field: edge.name})
				if meta != nil {
					meta = bashPPLayoutGet(meta.mapping, edge.name)
				}
				if edge.pointer && i+1 < len(sel.edges) {
					ptr.path = append(ptr.path, bashPPPointerStep{deref: true})
					meta = nil
				}
			}
			typ = sel.fieldType
			return nil
		case *syntax.BashPPIndexExpr:
			if err := descend(x.X); err != nil {
				return err
			}
			// Go permits indexing a pointer to an array as shorthand for
			// indexing the array itself: p[i] is (*p)[i]. Keep that implicit
			// indirection in the stored address path, just as selectors above
			// do for pointer bases. This is ordinary typed pointer semantics;
			// it deliberately does not reinterpret unsafe.Pointer storage.
			if pointer, isPointer := r.bashPPUnderlyingType(typ).(*syntax.BashPPPointerType); isPointer {
				array, isArray := r.bashPPUnderlyingType(pointer.Element).(*syntax.BashPPCollectionType)
				if isArray && array.Kind == "array" {
					ptr.path = append(ptr.path, bashPPPointerStep{deref: true})
					meta = nil
					typ = pointer.Element
				}
			}
			// A defined type such as `type bag []int` indexes exactly as its
			// underlying collection does, so the shape has to be read through
			// the definition rather than off the declared name.
			collection, found := r.bashPPUnderlyingType(typ).(*syntax.BashPPCollectionType)
			if !found {
				return fmt.Errorf("BASHPP-EPOINTER-TARGET: indexed target is not a collection")
			}
			if collection.Kind == "map" {
				return fmt.Errorf("BASHPP-ENONADDRESSABLE: map elements are not addressable")
			}
			index, err := r.bashPPCollectionIndex(x.Index)
			if err != nil {
				return err
			}
			var value any
			if r.bashPPGoSource {
				// descend has already evaluated every inner index. Read the
				// saved path instead of executing those operands again.
				value, _, _, err = ptr.read()
			} else {
				value, _, err = r.bashPPReadExpr(x.X)
			}
			if err != nil {
				return err
			}
			seq := value.([]any)
			if index.outOfBounds(len(seq)) {
				if r.bashPPGoSource {
					return r.bashPPSprint162CollectionBoundsPanic(x, index, len(seq))
				}
				return fmt.Errorf("BASHPP-ECOLLECTION-BOUNDS: index %s out of bounds for length %d", index.text, len(seq))
			}
			i := index.value
			ptr.path = append(ptr.path, bashPPPointerStep{index: i})
			typ = collection.Element
			if meta != nil {
				meta = meta.sequence[i]
			}
			return nil
		default:
			return fmt.Errorf("%sBASHPP-ENONADDRESSABLE: operand is not addressable (%T)", r.bashErrPrefix(node.Pos()), node)
		}
	}
	if err := descend(expr); err != nil {
		return nil, err
	}
	ptr.elem = typ
	return ptr, nil
}

func bashPPInferredCellType(cell *bashPPCell) syntax.BashPPTypeExpr {
	name := cell.typeName
	if name == "" {
		value := bashPPScalarFromString(cell.vr.String()).value
		switch value.Kind() {
		case constant.Bool:
			name = "bool"
		case constant.Int:
			name = "int"
		case constant.Float:
			name = "float64"
		default:
			name = "string"
		}
	}
	return &syntax.BashPPNamedType{Name: &syntax.Lit{Value: name}}
}

func (p *bashPPPointer) read() (any, *bashPPCollectionMeta, syntax.BashPPTypeExpr, error) {
	if err := goSourceUnsafeDerefCheck(p); err != nil {
		return nil, nil, nil, err
	}
	if p == nil || p.target == nil {
		return nil, nil, nil, errBashPPNilDereference
	}
	var value any
	// Head storage is four fields of one value (the pointer flag, the payload
	// carrier, the meta edges). A cell shared with an interpreted goroutine is
	// read through one snapshot so they cannot disagree; see
	// bashpp_cell_share.go.
	target := p.target.view()
	meta := bashPPCellMeta(target)
	if target.pointer {
		value = target.pointerValue
	} else if target.vr.Kind == expand.Object {
		value = target.vr.Obj
	} else {
		value = bashPPScalarValue(target.vr.String())
	}
	for _, step := range p.path {
		if step.deref {
			pointer, ok := value.(*bashPPPointer)
			if !ok || pointer == nil {
				return nil, nil, nil, errBashPPNilEmbeddedDereference
			}
			var err error
			value, meta, _, err = pointer.read()
			if err != nil {
				return nil, nil, nil, err
			}
			continue
		}
		if step.field != "" {
			mapping, ok := value.(map[string]any)
			if !ok {
				return nil, nil, nil, fmt.Errorf("BASHPP-EPOINTER-TARGET: pointer field path no longer names struct storage")
			}
			var found bool
			value, found = bashPPStorageGet(mapping, step.field)
			if !found {
				return nil, nil, nil, fmt.Errorf("BASHPP-EPOINTER-TARGET: pointer field %q no longer exists", step.field)
			}
			if meta != nil {
				meta = bashPPLayoutGet(meta.mapping, step.field)
			}
		} else {
			seq, ok := value.([]any)
			if !ok || step.index < 0 || step.index >= len(seq) {
				return nil, nil, nil, fmt.Errorf("BASHPP-EPOINTER-TARGET: pointer index no longer names collection storage")
			}
			value = seq[step.index]
			if meta != nil {
				meta = meta.sequence[step.index]
			}
		}
	}
	if p.unsafeOverlay() != nil {
		var err error
		value, meta, err = p.unsafeOverlay().read(p)
		if err != nil {
			return nil, nil, nil, err
		}
	} else if p.unsafeSource() != nil && bashPPTypeText(p.unsafeSource()) != bashPPTypeText(p.elem) {
		if p.unsafeSlice() != nil && p.unsafeSlice().target == p.elem {
			var err error
			if value, meta, err = p.unsafeSlice().read(value); err != nil {
				return nil, nil, nil, err
			}
		} else if p.unsafeView() != nil {
			value, meta = bashPPUnsafeBlankZero(p.unsafeView())
		} else {
			return nil, nil, nil, fmt.Errorf("BASHPP-EUNSAFE-VIEW: target %s is not a supported blank-field struct", bashPPTypeText(p.elem))
		}
	}
	// Dereferencing a pointer to an interface yields the interface value
	// itself: the target cell's interface edge travels with a direct read.
	// Without it a nil interface reads back as a bare value that can neither
	// compare with nil nor match `case nil`, and a live one re-boxes under
	// its static interface type instead of its dynamic type. A path that
	// walked into nested storage already carries that step's own metadata.
	if len(p.path) == 0 && (meta == nil || meta.interfaceValue == nil) {
		if iv := target.interfaceValue; iv != nil {
			typ := target.declType
			if typ == nil {
				typ = p.elem
			}
			meta = &bashPPCollectionMeta{kind: "interface", typ: typ, interfaceValue: iv}
		}
	}
	return value, meta, p.elem, nil
}

// slot resolves the pointer to the storage slot it addresses: the []any
// backing and index of its final step. Two spellings of one address — &a.T[0]
// and &b[0].T[0] after b's element shared a's slice backing — walk different
// paths to the same slot, which is the aliasing Go compares. Only an index
// step has an addressable slot; struct fields live in map storage, whose
// entries have no address, so any other final step reports false.
func (p *bashPPPointer) slot() ([]any, int, bool) {
	if p == nil || p.target == nil || len(p.path) == 0 || p.forged() || p.unsafeOffset() != 0 {
		return nil, 0, false
	}
	last := p.path[len(p.path)-1]
	if last.deref || last.field != "" {
		return nil, 0, false
	}
	var value any
	if p.target.pointer {
		value = p.target.pointerValue
	} else if p.target.vr.Kind == expand.Object {
		value = p.target.vr.Obj
	} else {
		return nil, 0, false
	}
	for _, step := range p.path[:len(p.path)-1] {
		switch {
		case step.deref:
			pointer, ok := value.(*bashPPPointer)
			if !ok || pointer == nil {
				return nil, 0, false
			}
			var err error
			value, _, _, err = pointer.read()
			if err != nil {
				return nil, 0, false
			}
		case step.field != "":
			mapping, ok := value.(map[string]any)
			if !ok {
				return nil, 0, false
			}
			value, ok = bashPPStorageGet(mapping, step.field)
			if !ok {
				return nil, 0, false
			}
		default:
			seq, ok := value.([]any)
			if !ok || step.index < 0 || step.index >= len(seq) {
				return nil, 0, false
			}
			value = seq[step.index]
		}
	}
	seq, ok := value.([]any)
	if !ok || last.index < 0 || last.index >= len(seq) {
		return nil, 0, false
	}
	return seq, last.index, true
}

// bashPPSliceArrayPointerValue presents a slice-to-array pointer's backing
// storage as the converted array on a Go-source dereference.
func (r *Runner) bashPPSliceArrayPointerValue(ptr *bashPPPointer, value any, meta *bashPPCollectionMeta) (any, *bashPPCollectionMeta, error) {
	if !r.bashPPGoSource || ptr == nil || meta == nil || meta.kind != "slice" {
		return value, meta, nil
	}
	array, ok := r.bashPPUnderlyingType(ptr.elem).(*syntax.BashPPCollectionType)
	if !ok || array.Kind != "array" || array.Length == nil {
		return value, meta, nil
	}
	n, err := r.bashPPArrayLength(array.Length.Value)
	if err != nil {
		return nil, nil, err
	}
	sequence, ok := value.([]any)
	if !ok || len(sequence) < n {
		return nil, nil, fmt.Errorf("BASHPP-EPOINTER-TARGET: pointer array storage no longer names collection storage")
	}
	converted := *meta
	converted.kind, converted.typ = "array", ptr.elem
	converted.sequence = append([]*bashPPCollectionMeta(nil), meta.sequence[:n]...)
	return sequence[:n], &converted, nil
}

// bashPPWriteSliceArrayPointer writes a whole array through a pointer made by
// converting a slice. The pointer's target cell is the captured slice header;
// replacing that cell would detach the pointer from its backing storage.
// rhs has already been evaluated with array value semantics, so copying it
// element-by-element also provides Go's copy-before-write overlap behavior.
func (r *Runner) bashPPWriteSliceArrayPointer(ptr *bashPPPointer, rhs any, rhsMeta *bashPPCollectionMeta) (bool, error) {
	if !r.bashPPGoSource || ptr == nil || ptr.target == nil || len(ptr.path) != 0 {
		return false, nil
	}
	targetMeta := bashPPCellMeta(ptr.target)
	if targetMeta == nil || targetMeta.kind != "slice" {
		return false, nil
	}
	array, ok := r.bashPPUnderlyingType(ptr.elem).(*syntax.BashPPCollectionType)
	if !ok || array.Kind != "array" || array.Length == nil {
		return false, nil
	}
	n, err := r.bashPPArrayLength(array.Length.Value)
	if err != nil {
		return true, err
	}
	target, targetOK := ptr.target.vr.Obj.([]any)
	source, sourceOK := rhs.([]any)
	if !targetOK || !sourceOK || len(target) < n || len(source) != n || len(targetMeta.sequence) < n || rhsMeta == nil || len(rhsMeta.sequence) < n {
		return true, fmt.Errorf("BASHPP-EPOINTER-TARGET: pointer array storage no longer names collection storage")
	}
	copy(target[:n], source)
	copy(targetMeta.sequence[:n], rhsMeta.sequence[:n])
	return true, nil
}

func bashPPScalarValue(text string) any {
	v := bashPPScalarFromString(text).value
	switch v.Kind() {
	case constant.Bool:
		return v.String() == "true"
	case constant.String:
		return constant.StringVal(v)
	case constant.Int:
		i, _ := constant.Int64Val(v)
		return int(i)
	case constant.Float:
		f, _ := constant.Float64Val(v)
		return f
	}
	return text
}

// bashPPGoSourceCollectionCarrier keeps a typed Go collection in its native
// interpreter payload instead of sending it through expand.NewObject's JSON
// safety policy. That policy is the right shell-object boundary, but its depth
// and traversal limits are not Go collection limits. Both the runtime payload
// and the authenticated collection shape must agree before this boundary is
// bypassed; an arbitrary invalid object is never admitted here.
func (r *Runner) bashPPGoSourceCollectionCarrier(value any, meta *bashPPCollectionMeta) (expand.Variable, bool) {
	if !r.bashPPGoSource || meta == nil || meta.typ == nil {
		return expand.Variable{}, false
	}
	shape, ok := r.bashPPUnderlyingType(meta.typ).(*syntax.BashPPCollectionType)
	if !ok || shape.Kind != meta.kind {
		return expand.Variable{}, false
	}
	switch shape.Kind {
	case "array", "inferred-array", "slice":
		if _, ok := value.([]any); !ok {
			return expand.Variable{}, false
		}
	case "map":
		if _, ok := value.(map[string]any); !ok {
			return expand.Variable{}, false
		}
	default:
		return expand.Variable{}, false
	}
	return expand.Variable{Set: true, Kind: expand.Object, Obj: value}, true
}

func (r *Runner) bashPPGoSourceObjectCarrier(value any, meta *bashPPCollectionMeta) (expand.Variable, bool) {
	if meta != nil && meta.kind == "struct" && meta.typ != nil {
		if _, _, ok := r.bashPPStructFields(meta.typ); ok {
			if _, ok := value.(map[string]any); ok {
				return expand.Variable{Set: true, Kind: expand.Object, Obj: value}, true
			}
		}
	}
	return r.bashPPGoSourceCollectionCarrier(value, meta)
}

// bashPPStoreCellValue publishes one value, with its shape, into cell.
//
// Every field it sets is part of ONE value: the payload carrier, the pointer
// triple, the meta edges and the scalar carrier flags have to change together
// or a reader sees a binding that was never assigned. So they are published as
// a bundle when the cell is one an interpreted goroutine can also reach; see
// bashpp_cell_share.go.
func bashPPStoreCellValue(cell *bashPPCell, value any, meta *bashPPCollectionMeta) {
	// Build the new binding in a private cell and store it in one go. Doing the
	// work on a copy is what keeps the guarded region to a single struct store:
	// the body below renders text, validates object payloads and reads the
	// interface payload's own cell, none of which may happen under a guard.
	next := *cell.view()
	bashPPFillCellValue(&next, value, meta)
	cell.publish(&next)
}

// bashPPFillCellValue is [bashPPStoreCellValue] on a cell no other goroutine
// can see yet. It is a separate function so that every path out of it — there
// are seven — lands on the single publishing store above.
func bashPPFillCellValue(cell *bashPPCell, value any, meta *bashPPCollectionMeta) {
	if meta != nil && meta.interfaceValue != nil {
		cell.pointer, cell.pointerValue, cell.nilPointer = false, nil, false
		cell.interfaceValue = meta.interfaceValue
		cell.valueMeta = nil
		cell.object = nil
		if meta.interfaceValue.nilIface || meta.interfaceValue.cell == nil {
			cell.vr = expand.Variable{Set: true, Kind: expand.String}
		} else {
			cell.vr = meta.interfaceValue.cell.viewVar()
		}
		return
	}
	cell.interfaceValue = nil
	if meta != nil && meta.kind == "channel" {
		// A channel read out of a collection has to arrive as the channel it
		// names, not as a rendering of one: an interpreter-owned channel lives
		// entirely in the identity beside the payload, while a dependency-owned
		// one is the handle payload itself. Either way the cell is what send,
		// receive, close and select resolve against.
		cell.pointer, cell.pointerValue, cell.nilPointer = false, nil, false
		cell.valueMeta, cell.channel, cell.channelOwner = meta, meta.channel, meta.channelOwner
		if native, ok := value.(*bashPPBridgeValue); ok && native != nil {
			cell.vr = expand.NewObject(native)
			return
		}
		text := ""
		if value != nil {
			text = fmt.Sprint(value)
		}
		cell.vr = expand.Variable{Set: true, Kind: expand.String, Str: text}
		return
	}
	if ptr, ok := value.(*bashPPPointer); ok || value == nil {
		if _, pointerType := cell.declType.(*syntax.BashPPPointerType); pointerType || meta != nil && meta.kind == "pointer" {
			cell.pointer, cell.pointerValue, cell.nilPointer = true, ptr, ptr == nil
			cell.vr = expand.Variable{Set: true, Kind: expand.String}
			return
		}
	}
	cell.pointer, cell.pointerValue, cell.nilPointer = false, nil, false
	if bashPPScalarMetaKind(meta) {
		cell.vr = expand.Variable{Set: true, Kind: expand.String, Str: fmt.Sprint(value)}
		cell.valueMeta, cell.object = nil, nil
		cell.exactScalar, cell.negativeZero = nil, false
		cell.nonFinite, cell.hasNonFinite = 0, false
		cell.nonFiniteComplex, cell.hasNonFiniteComplex = 0, false
		if meta.typ != nil {
			cell.declType = meta.typ
		}
		if named, ok := cell.declType.(*syntax.BashPPNamedType); ok && named.Name != nil {
			cell.typeName = named.Name.Value
		}
		cell.scalarKind = bashPPScalarKindForType(cell.declType)
		if cell.scalarKind == constant.Unknown {
			switch value.(type) {
			case string:
				if cell.declType == nil {
					cell.scalarKind = constant.String
				}
			case bool:
				cell.scalarKind = constant.Bool
			case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, uintptr:
				cell.scalarKind = constant.Int
			case float32, float64:
				cell.scalarKind = constant.Float
			case complex64, complex128:
				cell.scalarKind = constant.Complex
			}
		}
		return
	}
	if meta != nil && meta.kind == "func" {
		cell.valueMeta = meta
		if native, ok := value.(*bashPPBridgeValue); ok && native != nil {
			cell.vr = expand.NewObject(native)
			return
		}
		cell.vr = expand.Variable{Set: true, Kind: expand.String, Str: fmt.Sprint(value)}
		return
	}
	if meta != nil {
		// Collection payloads are interpreter-owned typed storage. They may
		// legitimately contain values which cannot cross the shell's JSON object
		// boundary (complex numbers, functions, channels, and native handles).
		// expand.NewObject validates that public boundary and replaces such a
		// payload with expand.invalidObject, which destroys the value while its
		// collection metadata still claims the original shape. Keep trusted
		// collection storage verbatim; expansion still applies ObjectString when
		// a value actually crosses into an ordinary shell word.
		cell.vr, cell.valueMeta = bashPPCollectionVariable(value), meta
		if cell.object == nil {
			cell.object = &bashPPObjectIdentity{collection: meta}
		}
		return
	}
	if native, ok := value.(*bashPPBridgeValue); ok && native != nil {
		// A native handle read out of a func-typed collection element arrives
		// with no collection metadata of its own: the map stored the dependency
		// function value (dep.Init) as the bridge handle it is. It must be kept
		// as that handle Object so a later `f = m[k]` or `m[k](...)` still calls
		// the dependency; fmt.Sprint would flatten the descriptor to text and a
		// native call would then be handed a "string" for its func(*T) parameter.
		cell.vr = expand.NewObject(native)
		return
	}
	cell.vr = expand.Variable{Set: true, Kind: expand.String, Str: fmt.Sprint(value)}
}

func bashPPCollectionVariable(value any) expand.Variable {
	return expand.Variable{Set: true, Kind: expand.Object, Obj: value}
}

func (r *Runner) bashPPBindPointerExpr(name string, expr syntax.BashPPExpr) bool {
	var value any
	var meta *bashPPCollectionMeta
	var typ syntax.BashPPTypeExpr
	if ptr, target, handled, err := r.goSourceUnsafePointerExpr(expr); handled {
		if err != nil {
			r.errf("%v\n", err)
			r.exit.code = 2
			return true
		}
		expr, value, typ, meta = nil, ptr, target, bashPPPointerMeta(target)
	} else if ptr, handled, err := r.goSourceUnsafeDataPointer(expr); handled {
		if err != nil {
			r.errf("%v\n", err)
			r.exit.code = 2
			return true
		}
		typ, _ = r.goSourceStaticExprType(expr)
		if ptr != nil {
			typ = &syntax.BashPPPointerType{Element: ptr.elem}
		}
		expr, value, meta = nil, ptr, bashPPPointerMeta(typ)
	}
	if ptr, target, converted, err := r.bashPPPointerConversion(expr); converted {
		if err != nil {
			r.errf("%v\n", err)
			r.exit.code = 2
			return true
		}
		if ptr == nil {
			// A runtime nil operand — `Peano(q)` with q nil — binds as a
			// nil pointer of the conversion's type, like the retype below.
			expr, typ, meta = nil, target, bashPPPointerMeta(target)
		} else {
			expr, value, typ = nil, ptr, target
		}
	}
	switch x := expr.(type) {
	case nil:
	case *syntax.BashPPAddressExpr, *syntax.BashPPNewExpr:
		ptr, err := r.bashPPPointerExprValue(expr)
		if err != nil {
			r.errf("%v\n", err)
			r.exit.code = 2
			return true
		}
		value, typ = ptr, &syntax.BashPPPointerType{Element: ptr.elem}
	case *syntax.BashPPIdent:
		operand := r.bashPPScope.lookup(x.Name.Value).view()
		if operand == nil || !operand.pointer {
			return false
		}
		value, typ = operand.pointerValue, operand.declType
	case *syntax.BashPPDerefExpr:
		ptr, err := r.bashPPPointerExprValue(x.X)
		if err != nil {
			r.errf("%v\n", err)
			r.exit.code = 2
			return true
		}
		if ptr == nil {
			r.bashPPReportNilDereference()
			return true
		}
		value, meta, typ, err = ptr.read()
		if err != nil {
			r.errf("%v\n", err)
			r.exit.code = 2
			return true
		}
		// A dereference produces the pointed-to value. Structs and arrays are
		// values in Go, so binding that result must copy their outer storage;
		// fields which are themselves pointers, slices, or maps still retain
		// their reference identity through bashPPCopyArrayValue. Storing the
		// map/sequence returned by ptr.read directly made `u := *p` alias the
		// pointee's whole aggregate, so a later field assignment through p also
		// rewrote u.
		value, meta = bashPPCopyArrayValue(value, meta)
	default:
		return false
	}
	r.bashPPDeclareName(name, expand.Variable{Set: true, Kind: expand.String})
	cell := r.bashPPScope.lookup(name)
	cell.declType = typ
	// Defined pointer types retain their named declaration, so storage must
	// carry pointer metadata explicitly rather than infer from syntax shape.
	if _, pointer := r.bashPPPointerType(typ); pointer {
		meta = bashPPPointerMeta(typ)
	}
	if ptrType, ok := typ.(*syntax.BashPPPointerType); ok {
		if named, ok := ptrType.Element.(*syntax.BashPPNamedType); ok {
			cell.typeName = named.Name.Value
		}
	}
	bashPPStoreCellValue(cell, value, meta)
	if stored := cell.view(); stored.object != nil && stored.object.owner == "" {
		// The identity pointer keeps its identity through the snapshot, so
		// naming its owner through one is the same store.
		stored.object.owner = name
	}
	return true
}

func bashPPUnparenExpr(expr syntax.BashPPExpr) syntax.BashPPExpr {
	for {
		paren, ok := expr.(*syntax.BashPPParenExpr)
		if !ok {
			return expr
		}
		expr = paren.X
	}
}

func (r *Runner) bashPPDerefAssign(target *syntax.BashPPDerefExpr, rhs syntax.BashPPExpr) {
	ptr, err := r.bashPPPointerExprValue(target.X)
	if err == nil && ptr == nil {
		err = r.goSourceRuntimeFault(errBashPPNilDereference)
	}
	if err != nil {
		if handled, nativeErr := r.goSourceNativePointeeAssign(err, rhs); handled {
			if nativeErr != nil && !errors.Is(nativeErr, errBashPPScalarInterrupted) {
				r.errf("%v\n", nativeErr)
				r.exit.code = 2
			}
			return
		}
		if !errors.Is(err, errBashPPScalarInterrupted) {
			r.errf("%v\n", err)
			r.exit.code = 2
		}
		return
	}
	if err := goSourceUnsafeDerefCheck(ptr); err != nil {
		r.errf("%v\n", err)
		r.exit.code = 2
		return
	}
	if ptr.unsafeSlice() != nil && ptr.unsafeSlice().target == ptr.elem {
		r.errf("BASHPP-EUNSAFE-WRITE: writes through a reinterpreted slice header are unsupported\n")
		r.exit.code = 2
		return
	}
	if ptr.unsafeSource() != nil && bashPPTypeText(ptr.unsafeSource()) != bashPPTypeText(ptr.elem) && ptr.unsafeView() == nil && ptr.unsafeOverlay() == nil {
		r.errf("BASHPP-EUNSAFE-VIEW: target %s is not a supported blank-field struct\n", bashPPTypeText(ptr.elem))
		r.exit.code = 2
		return
	}
	if ptr.unsafeView() != nil {
		r.errf("BASHPP-EUNSAFE-WRITE: writes through reinterpreted blank views are unsupported\n")
		r.exit.code = 2
		return
	}
	if ptr.unsafeOverlay() != nil && ptr.unsafeOverlay().target.blank {
		r.errf("BASHPP-EUNSAFE-WRITE: writes through reinterpreted blank views cannot preserve blank-field bytes\n")
		r.exit.code = 2
		return
	}
	value, meta, err := r.bashPPEvalTypedValue(rhs, ptr.elem)
	if err != nil {
		// An interrupted right-hand side (a receive unwound by program
		// exit or a panic) already carries its outcome.
		if !errors.Is(err, errBashPPScalarInterrupted) {
			r.errf("BASHPP-EASSIGN-MISMATCH: %v\n", err)
			r.exit.code = 2
		}
		return
	}
	if ptr.target.object != nil && ptr.target.object.readonly {
		r.errf("BASHPP-EREADONLY-MUTATION: cannot mutate readonly value %q through pointer\n", ptr.target.object.owner)
		r.exit.code = 2
		return
	}
	if ptr.target.constant || ptr.target.vr.ReadOnly {
		r.errf("BASHPP-EREADONLY-MUTATION: cannot mutate readonly value through pointer\n")
		r.exit.code = 2
		return
	}
	if len(ptr.path) == 0 {
		if ptr.unsafeOverlay() != nil {
			if err := ptr.unsafeOverlay().write(ptr, value, meta); err != nil {
				r.errf("%v\n", err)
				r.exit.code = 2
			}
			return
		}
		if handled, err := r.bashPPWriteSliceArrayPointer(ptr, value, meta); handled {
			if err != nil {
				r.errf("%v\n", err)
				r.exit.code = 2
			}
			return
		}
		bashPPStoreCellValue(ptr.target, value, meta)
		return
	}
	parent, parentMeta, _, err := ptr.readParent()
	if err != nil {
		r.errf("%v\n", err)
		r.exit.code = 2
		return
	}
	last := ptr.path[len(ptr.path)-1]
	if last.field != "" {
		mapping, ok := parent.(map[string]any)
		if !ok {
			r.errf("BASHPP-EPOINTER-TARGET: pointer field path no longer names struct storage\n")
			r.exit.code = 2
			return
		}
		var layout map[string]*bashPPCollectionMeta
		if parentMeta != nil {
			layout = parentMeta.mapping
		}
		bashPPStorageSetField(mapping, layout, last.field, value, meta)
	} else {
		sequence, ok := parent.([]any)
		if !ok || last.index < 0 || last.index >= len(sequence) {
			r.errf("BASHPP-EPOINTER-TARGET: pointer index no longer names collection storage\n")
			r.exit.code = 2
			return
		}
		sequence[last.index] = value
		if parentMeta != nil {
			parentMeta.sequence[last.index] = meta
		}
	}
}

func (p *bashPPPointer) readParent() (any, *bashPPCollectionMeta, syntax.BashPPTypeExpr, error) {
	copy := *p
	copy.path = p.path[:len(p.path)-1]
	return copy.read()
}

// goSourcePointeeFieldAddress is the address of field name selected through
// the pointer base: Go's implicit dereference of a pointer operand.
func (r *Runner) goSourcePointeeFieldAddress(base *bashPPPointer, name string) (*bashPPPointer, error) {
	if base == nil {
		return nil, errBashPPNilDereference
	}
	if base.unsafeOverlay() != nil {
		return base.unsafeOverlay().field(base, name)
	}
	sel := r.bashPPResolveField(base.elem, name)
	if sel.ambiguous || len(sel.edges) == 0 {
		return nil, bashPPSelectionError(base.elem, name, sel)
	}
	ptr := &bashPPPointer{target: base.target, path: append([]bashPPPointerStep(nil), base.path...)}
	for i, edge := range sel.edges {
		ptr.path = append(ptr.path, bashPPPointerStep{field: edge.name})
		if edge.pointer && i+1 < len(sel.edges) {
			ptr.path = append(ptr.path, bashPPPointerStep{deref: true})
		}
	}
	ptr.elem = sel.fieldType
	return ptr, nil
}
