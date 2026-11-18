// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"errors"
	"fmt"
	"go/constant"
	"math/bits"
	"strings"
	"sync"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// The unsafe builtins over interpreter storage use a storage-span model. An
// unsafe.Pointer made from `&a[i]` is the same *bashPPPointer the address
// names: the backing cell, the path to the element, and the element's type.
// The span is the []any backing that element lives in. unsafe.Add moves the
// element index when the byte delta is a whole number of elements and the
// result stays within the span (one past the end included); any other
// delta is kept as a residual byte offset, which still compares by value but
// refuses every dereference. unsafe.Slice and unsafe.String over a span read
// or alias the real backing, so writes through the slice are writes to the
// original storage.
//
// An unsafe.Pointer converted from a nonzero integer is a forged address. It
// exists only as an opaque value: it compares, moves and takes part in the
// address-space overflow checks Go makes, and every dereference, slice or
// string over it refuses. The interpreter never reads or writes through it.

var errGoSourceUnsafeForged = errors.New("BASHPP-EUNSAFE-FORGED: pointer made from an integer address names no interpreter storage")

var errGoSourceUnsafeSpan = errors.New("BASHPP-EUNSAFE-SPAN: pointer arithmetic left the storage span it was derived from")

// goSourceUnsafeCallName reports the unsafe builtin a call names, authenticating
// the import and requiring want arguments.
func (r *Runner) goSourceUnsafeCallName(expr syntax.BashPPExpr, want int) (string, *syntax.BashPPCall, bool) {
	call, ok := bashPPUnparenExpr(expr).(*syntax.BashPPCall)
	if !ok || !r.bashPPGoSource || len(call.Fun) != 2 || len(call.ArgExprs) != want || call.Ellipsis.IsValid() {
		return "", nil, false
	}
	if r.bashPPImports[call.Fun[0].Value] != "unsafe" {
		return "", nil, false
	}
	if r.bashPPScope != nil && r.bashPPScope.lookup(call.Fun[0].Value) != nil {
		return "", nil, false
	}
	return call.Fun[1].Value, call, true
}

func goSourceUnsafePointerTypeFor(alias string) syntax.BashPPTypeExpr {
	return &syntax.BashPPNamedType{Name: &syntax.Lit{Value: alias + ".Pointer"}}
}

// goSourceUnsafePointerExpr evaluates an expression whose static type is
// unsafe.Pointer and which makes a new value: a conversion to
// unsafe.Pointer or a call to unsafe.Add. It reports the unsafe.Pointer type
// as spelled so comparisons keep one type text on both sides.
func (r *Runner) goSourceUnsafePointerExpr(expr syntax.BashPPExpr) (*bashPPPointer, syntax.BashPPTypeExpr, bool, error) {
	if !r.bashPPGoSource {
		return nil, nil, false, nil
	}
	expr = bashPPUnparenExpr(expr)
	if name, call, ok := r.goSourceUnsafeCallName(expr, 2); ok && name == "Add" {
		ptr, err := r.bashPPPointerExprValue(call.ArgExprs[0])
		if err != nil {
			return nil, nil, true, err
		}
		delta, err := r.goSourceUnsafeInt(call.ArgExprs[1])
		if err != nil {
			return nil, nil, true, err
		}
		moved, err := r.goSourceUnsafeMove(ptr, delta)
		return moved, goSourceUnsafePointerTypeFor(call.Fun[0].Value), true, err
	}
	conv, ok := expr.(*syntax.BashPPConvertExpr)
	if !ok {
		return nil, nil, false, nil
	}
	target := r.bashPPConvertTarget(conv)
	if !r.goSourceUnsafePointerType(target) {
		return nil, nil, false, nil
	}
	if goSourceNilLiteral(conv.X) {
		return nil, target, true, nil
	}
	if r.goSourceUnsafeIntegerOperand(conv.X) {
		address, err := r.goSourceUnsafeInt(conv.X)
		if err != nil {
			return nil, target, true, err
		}
		if address == 0 {
			return nil, target, true, nil
		}
		if ptr := r.goSourceUnsafeRegisteredAddress(uint64(address)); ptr != nil {
			return ptr, target, true, nil
		}
		return bashPPForgedPointer(uint64(address)), target, true, nil
	}
	if r.goSourceUnsafeReflectAddressOperand(conv.X) {
		address, err := r.goSourceUnsafeInt(conv.X)
		if err != nil {
			return nil, target, true, err
		}
		if address == 0 {
			return nil, target, true, nil
		}
		if ptr := r.goSourceUnsafeRegisteredAddress(uint64(address)); ptr != nil {
			return ptr, target, true, nil
		}
		return bashPPForgedPointer(uint64(address)), target, true, nil
	}
	ptr, err := r.bashPPPointerExprValue(conv.X)
	if err != nil && strings.HasPrefix(err.Error(), "BASHPP-EPOINTER-TARGET:") {
		// An operand that is neither a recognised integer spelling nor
		// interpreter storage is never trusted as an address.
		return nil, target, true, fmt.Errorf("BASHPP-EUNSAFE-ADDRESS: unsafe.Pointer operand is not an interpreter-owned live pointer (%v)", err)
	}
	if err != nil || ptr == nil {
		return nil, target, true, err
	}
	view := *ptr
	if view.unsafeSource() == nil && !view.forged() {
		view.setUnsafeSource(ptr.elem)
	}
	return &view, target, true, nil
}

func (r *Runner) goSourceUnsafeRegisteredAddress(address uint64) *bashPPPointer {
	if r.bashPPTools.bridge == nil {
		return nil
	}
	session := r.bashPPTools.bridge
	session.mu.Lock()
	ptr := session.addressOrigins[address]
	session.mu.Unlock()
	if ptr == nil {
		return nil
	}
	view := *ptr
	if view.unsafeSource() == nil {
		view.setUnsafeSource(ptr.elem)
	}
	return &view
}

func (r *Runner) goSourceUnsafeReflectAddressOperand(expr syntax.BashPPExpr) bool {
	call, ok := bashPPUnparenExpr(expr).(*syntax.BashPPCall)
	if !ok || len(call.ArgExprs) != 0 || call.Ellipsis.IsValid() {
		return false
	}
	if selector, ok := call.CalleeExpr.(*syntax.BashPPSelectorExpr); ok {
		return selector.Sel.Value == "Pointer" || selector.Sel.Value == "UnsafeAddr"
	}
	if len(call.Fun) > 0 {
		name := call.Fun[len(call.Fun)-1].Value
		return name == "Pointer" || name == "UnsafeAddr"
	}
	return false
}

// goSourceUnsafeIntegerOperand reports whether a conversion operand is an
// integer (uintptr) expression rather than a pointer. Only integer spellings
// qualify: a conversion to an integer type, an arithmetic or bitwise
// operator, a literal, or a variable declared with an integer type.
func (r *Runner) goSourceUnsafeIntegerOperand(expr syntax.BashPPExpr) bool {
	switch x := bashPPUnparenExpr(expr).(type) {
	case *syntax.BashPPBasicLit, *syntax.BashPPBinaryExpr:
		return true
	case *syntax.BashPPUnaryExpr:
		return x.Op == nil || x.Op.Value != "<-"
	case *syntax.BashPPConvertExpr:
		return goSourceUnsafeIntegerType(r.bashPPUnderlyingType(r.bashPPConvertTarget(x)))
	case *syntax.BashPPIdent:
		cell := r.bashPPScope.lookup(x.Name.Value).view()
		if cell == nil || cell.pointer {
			return false
		}
		if goSourceUnsafeIntegerType(r.bashPPUnderlyingType(cell.declType)) {
			return true
		}
		if cell.typeName != "" && goSourceUnsafeIntegerType(&syntax.BashPPNamedType{Name: &syntax.Lit{Value: cell.typeName}}) {
			return true
		}
		return cell.scalarKind == constant.Int
	}
	if typ, ok := r.goSourceStaticExprType(expr); ok {
		return goSourceUnsafeIntegerType(r.bashPPUnderlyingType(typ))
	}
	return false
}

func goSourceUnsafeIntegerType(typ syntax.BashPPTypeExpr) bool {
	named, ok := typ.(*syntax.BashPPNamedType)
	if !ok || named.Name == nil {
		return false
	}
	switch named.Name.Value {
	case "uintptr", "int", "int8", "int16", "int32", "int64", "uint", "uint8", "uint16", "uint32", "uint64", "byte", "rune":
		return true
	}
	return false
}

// goSourceUnsafeInt evaluates an integer operand exactly once. A value outside
// the int64/uint64 range reports ok=false through the error path of callers.
func (r *Runner) goSourceUnsafeInt(expr syntax.BashPPExpr) (int64, error) {
	value, err := r.bashPPEvalScalarExpr(expr)
	if err != nil {
		return 0, err
	}
	v := constant.ToInt(value.value)
	if n, ok := constant.Int64Val(v); ok {
		return n, nil
	}
	if u, ok := constant.Uint64Val(v); ok {
		return int64(u), nil
	}
	return 0, fmt.Errorf("BASHPP-EUNSAFE-INT: %s is not an integer address", value.value)
}

// goSourceUnsafePointerWord answers uintptr(unsafe.Pointer(p)) without
// exposing host addresses. The word is stable for one interpreter storage
// path and deliberately becomes a forged address if converted back through
// unsafe.Pointer: raw integer round-trips may compare or do arithmetic, but
// they no longer name interpreter-owned storage.
func (r *Runner) goSourceUnsafePointerWord(conv *syntax.BashPPConvertExpr) (bashPPScalar, bool, error) {
	target := r.bashPPConvertTarget(conv)
	if !goSourceUnsafeIntegerType(r.bashPPUnderlyingType(target)) {
		return bashPPScalar{}, false, nil
	}
	// Integer conversions are ordinary scalar expressions unless their
	// operand is a pointer value. Probing every operand through the pointer
	// evaluator would execute calls such as T(side()) a second time before
	// the scalar evaluator handles the conversion.
	if r.goSourceUnsafeIntegerOperand(conv.X) {
		return bashPPScalar{}, false, nil
	}
	ptr, err := r.bashPPPointerExprValue(conv.X)
	if err != nil {
		return bashPPScalar{}, false, nil
	}
	if ptr == nil {
		return bashPPScalar{value: constant.MakeInt64(0), typ: bashPPTypeText(target), runtime: true}, true, nil
	}
	return bashPPScalar{value: constant.MakeUint64(r.goSourceUnsafeOpaqueAddress(ptr)), typ: bashPPTypeText(target), runtime: true}, true, nil
}

type goSourceUnsafeOpaqueIDs struct {
	mu    sync.Mutex
	next  uint64
	cells map[*bashPPCell]uint64
}

func (r *Runner) goSourceUnsafeOpaqueAddress(ptr *bashPPPointer) uint64 {
	if ptr == nil {
		return 0
	}
	if ptr.forged() {
		return ptr.unsafeAddress()
	}
	addr := r.goSourceUnsafeOpaqueCellID(ptr.target)
	for _, step := range ptr.path {
		addr = goSourceUnsafeOpaqueMix(addr, uint64(step.index+17))
		if step.field != "" {
			for _, ch := range step.field {
				addr = goSourceUnsafeOpaqueMix(addr, uint64(ch))
			}
		}
		if step.deref {
			addr = goSourceUnsafeOpaqueMix(addr, 1)
		}
	}
	if ptr.unsafeOffset() != 0 {
		addr = goSourceUnsafeOpaqueMix(addr, uint64(ptr.unsafeOffset()))
	}
	if addr == 0 {
		return 1
	}
	return addr
}

func (r *Runner) goSourceUnsafeOpaqueCellID(cell *bashPPCell) uint64 {
	if cell == nil {
		return 1
	}
	ids := r.goSourceUnsafeOpaqueIDs
	if ids == nil {
		ids = &goSourceUnsafeOpaqueIDs{}
		r.goSourceUnsafeOpaqueIDs = ids
	}
	ids.mu.Lock()
	defer ids.mu.Unlock()
	if ids.cells == nil {
		ids.cells = make(map[*bashPPCell]uint64)
	}
	if id := ids.cells[cell]; id != 0 {
		return id
	}
	ids.next++
	id := ids.next + 0x10000
	ids.cells[cell] = id
	return id
}

func goSourceUnsafeOpaqueMix(base, part uint64) uint64 {
	const prime = 1099511628211
	return (base ^ (part + 0x9e3779b97f4a7c15)) * prime
}

// goSourceUnsafeElemSize is the gc size of the element type a span pointer
// was derived from.
func (r *Runner) goSourceUnsafeElemSize(ptr *bashPPPointer) (int64, bool) {
	elem := ptr.unsafeSource()
	if elem == nil {
		elem = ptr.elem
	}
	if elem == nil {
		return 0, false
	}
	shape, ok := r.bashPPGoShapeType(elem, 0)
	if !ok {
		return 0, false
	}
	return int64(shape.Size()), true
}

// goSourceUnsafeMove is unsafe.Add over the span model.
func (r *Runner) goSourceUnsafeMove(ptr *bashPPPointer, delta int64) (*bashPPPointer, error) {
	if ptr == nil {
		if delta == 0 {
			return nil, nil
		}
		return bashPPForgedPointer(uint64(delta)), nil
	}
	moved := *ptr
	moved.path = append([]bashPPPointerStep(nil), ptr.path...)
	if moved.forged() {
		moved.setUnsafeAddress(moved.unsafeAddress() + uint64(delta))
		return &moved, nil
	}
	total := moved.unsafeOffset() + delta
	moved.setUnsafeOffset(total)
	last := len(moved.path) - 1
	if last < 0 || moved.path[last].deref || moved.path[last].field != "" || moved.target == nil {
		return &moved, nil
	}
	size, ok := r.goSourceUnsafeElemSize(ptr)
	if !ok || size <= 0 || total%size != 0 {
		return &moved, nil
	}
	parent, _, _, err := ptr.readParent()
	if err != nil {
		return nil, err
	}
	seq, ok := parent.([]any)
	if !ok {
		return &moved, nil
	}
	index := int64(moved.path[last].index) + total/size
	if index < 0 || index > int64(len(seq)) {
		return &moved, nil
	}
	moved.path[last].index = int(index)
	moved.setUnsafeOffset(0)
	return &moved, nil
}

// goSourceUnsafeDerefCheck refuses a dereference of a forged or out-of-span
// pointer before any storage is touched.
func goSourceUnsafeDerefCheck(p *bashPPPointer) error {
	if p == nil {
		return nil
	}
	if p.forged() {
		return errGoSourceUnsafeForged
	}
	if p.unsafeRefusal() != nil && p.unsafeOverlay() == nil && p.unsafeSource() != nil && bashPPTypeText(p.unsafeSource()) != bashPPTypeText(p.elem) {
		return p.unsafeRefusal()
	}
	if p.unsafeOffset() != 0 {
		return errGoSourceUnsafeSpan
	}
	return nil
}

func goSourceUnsafeRuntime(name, message string) error {
	return &bashPPRuntimeError{refusal: "BASHPP-EUNSAFE-" + strings.ToUpper(name) + ": unsafe." + message, runtime: "unsafe." + message}
}

// goSourceUnsafeLength applies Go's run-time checks for unsafe.Slice and
// unsafe.String: the length is a non-negative int, a nil pointer takes only
// a zero length, and the byte extent may neither overflow nor run past the
// end of the address space. A forged address supplies its exact position;
// interpreter storage has no numeric address, so only the overflow is
// decided there and the span check that follows refuses any longer extent.
func (r *Runner) goSourceUnsafeLength(builtin string, ptr *bashPPPointer, lengthExpr syntax.BashPPExpr, size int64) (int64, error) {
	value, err := r.bashPPEvalScalarExpr(lengthExpr)
	if err != nil {
		return 0, err
	}
	n, ok := constant.Int64Val(constant.ToInt(value.value))
	if !ok || n < 0 {
		return 0, goSourceUnsafeRuntime(builtin, builtin+": len out of range")
	}
	if ptr == nil {
		if n == 0 {
			return 0, nil
		}
		return 0, goSourceUnsafeRuntime(builtin, builtin+": ptr is nil and len is not zero")
	}
	hi, mem := bits.Mul64(uint64(size), uint64(n))
	if hi != 0 {
		return 0, goSourceUnsafeRuntime(builtin, builtin+": len out of range")
	}
	if ptr.forged() && mem > -ptr.unsafeAddress() {
		return 0, goSourceUnsafeRuntime(builtin, builtin+": len out of range")
	}
	return n, nil
}

// goSourceUnsafeSpan resolves a span pointer to the backing it names and the
// element index it starts at.
func goSourceUnsafeSpan(ptr *bashPPPointer) ([]any, *bashPPCollectionMeta, int, error) {
	if err := goSourceUnsafeDerefCheck(ptr); err != nil {
		return nil, nil, 0, err
	}
	last := len(ptr.path) - 1
	if last < 0 || ptr.path[last].deref || ptr.path[last].field != "" {
		return nil, nil, 0, fmt.Errorf("BASHPP-EUNSAFE-SPAN: pointer does not name an element of array or slice storage")
	}
	parent, meta, _, err := ptr.readParent()
	if err != nil {
		return nil, nil, 0, err
	}
	seq, ok := parent.([]any)
	if !ok {
		return nil, nil, 0, fmt.Errorf("BASHPP-EUNSAFE-SPAN: pointer does not name an element of array or slice storage")
	}
	return seq, meta, ptr.path[last].index, nil
}

// goSourceUnsafeSliceValue answers unsafe.Slice over interpreter storage. The
// result aliases the span's backing: its []any shares the elements and its
// per-element metadata, capped at the requested length as Go's cap(s) == len.
func (r *Runner) goSourceUnsafeSliceValue(expr syntax.BashPPExpr) (value any, meta *bashPPCollectionMeta, claimed bool, err error) {
	name, call, ok := r.goSourceUnsafeCallName(expr, 2)
	if !ok || name != "Slice" {
		return nil, nil, false, nil
	}
	return r.goSourceUnsafeSlice(call, false)
}

// goSourceUnsafeSlice evaluates unsafe.Slice once. With discard set, a forged
// pointer that passes Go's checks yields no value instead of a refusal.
func (r *Runner) goSourceUnsafeSlice(call *syntax.BashPPCall, discard bool) (value any, meta *bashPPCollectionMeta, claimed bool, err error) {
	if address, ok := call.ArgExprs[0].(*syntax.BashPPAddressExpr); ok {
		if index, ok := address.X.(*syntax.BashPPIndexExpr); ok && r.bashPPNativeExpr(index.X) {
			return nil, nil, false, nil
		}
	}
	defer func() { err = r.goSourceRuntimeFault(err) }()
	ptr, err := r.bashPPPointerExprValue(call.ArgExprs[0])
	if err != nil {
		return nil, nil, true, err
	}
	ptrType, ok := r.bashPPPointerType(r.bashPPPointerExprType(call.ArgExprs[0], ptr))
	if !ok || ptrType.Element == nil {
		return nil, nil, true, fmt.Errorf("BASHPP-EUNSAFE-SLICE: pointer operand has no element type")
	}
	sliceType := &syntax.BashPPCollectionType{Kind: "slice", Element: ptrType.Element}
	shape, ok := r.bashPPGoShapeType(ptrType.Element, 0)
	if !ok {
		return nil, nil, true, fmt.Errorf("BASHPP-EUNSAFE-SLICE: element type %s has no layout", bashPPTypeText(ptrType.Element))
	}
	n, err := r.goSourceUnsafeLength("Slice", ptr, call.ArgExprs[1], int64(shape.Size()))
	if err != nil {
		return nil, nil, true, err
	}
	if ptr == nil {
		zero, zeroMeta := r.bashPPZeroValue(sliceType)
		return zero, zeroMeta, true, nil
	}
	if discard && ptr.forged() {
		return nil, nil, true, nil
	}
	seq, parentMeta, start, err := goSourceUnsafeSpan(ptr)
	if err != nil {
		return nil, nil, true, err
	}
	end := int64(start) + n
	if end > int64(len(seq)) {
		return nil, nil, true, fmt.Errorf("BASHPP-EUNSAFE-SPAN: unsafe.Slice of %d elements runs past the %d-element storage span", n, len(seq)-start)
	}
	out := seq[start:int(end):int(end)]
	meta = &bashPPCollectionMeta{kind: "slice", typ: sliceType}
	if parentMeta != nil && len(parentMeta.sequence) >= int(end) {
		meta.sequence = parentMeta.sequence[start:int(end):int(end)]
	} else {
		meta.sequence = make([]*bashPPCollectionMeta, n)
	}
	return out, meta, true, nil
}

// goSourceUnsafeDataPointer answers unsafe.StringData and unsafe.SliceData.
// StringData names a read-only byte array holding the string's bytes: Go
// forbids writes through it, and strings are immutable, so the copy is
// unobservable. SliceData names element 0 of the slice's own backing,
// evaluated once and held in an anonymous cell that keeps the backing
// shared, as bashPPSliceToArrayPointer does.
func (r *Runner) goSourceUnsafeDataPointer(expr syntax.BashPPExpr) (*bashPPPointer, bool, error) {
	name, call, ok := r.goSourceUnsafeCallName(expr, 1)
	if !ok || (name != "StringData" && name != "SliceData") {
		return nil, false, nil
	}
	byteType := &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "byte"}}
	if name == "StringData" {
		value, err := r.bashPPEvalScalarExpr(call.ArgExprs[0])
		if err != nil {
			return nil, true, err
		}
		text := constant.StringVal(value.value)
		if text == "" {
			return nil, true, fmt.Errorf("BASHPP-EUNSAFE-STRINGDATA: unsafe.StringData of an empty string names no interpreter storage")
		}
		arrayType := &syntax.BashPPCollectionType{Kind: "array", Element: byteType, Length: &syntax.Lit{Value: fmt.Sprint(len(text))}}
		seq := make([]any, len(text))
		metas := make([]*bashPPCollectionMeta, len(text))
		for i := range len(text) {
			seq[i] = int(text[i])
		}
		cell := &bashPPCell{declType: arrayType}
		bashPPStoreCellValue(cell, seq, &bashPPCollectionMeta{kind: "array", typ: arrayType, sequence: metas})
		return &bashPPPointer{target: cell, path: []bashPPPointerStep{{index: 0}}, elem: byteType}, true, nil
	}
	value, meta, err := r.bashPPReadExpr(call.ArgExprs[0])
	if err != nil {
		return nil, true, err
	}
	if meta == nil || meta.kind != "slice" {
		return nil, true, fmt.Errorf("BASHPP-EUNSAFE-SLICEDATA: operand is not a slice")
	}
	sliceType, ok := r.bashPPUnderlyingType(meta.typ).(*syntax.BashPPCollectionType)
	if !ok {
		return nil, true, fmt.Errorf("BASHPP-EUNSAFE-SLICEDATA: operand is not a slice")
	}
	seq, _ := value.([]any)
	if seq == nil {
		return nil, true, nil
	}
	if len(seq) == 0 {
		return nil, true, fmt.Errorf("BASHPP-EUNSAFE-SLICEDATA: unsafe.SliceData of an empty slice names no element storage")
	}
	cell := &bashPPCell{declType: meta.typ}
	bashPPStoreCellValue(cell, value, meta)
	return &bashPPPointer{target: cell, path: []bashPPPointerStep{{index: 0}}, elem: sliceType.Element}, true, nil
}

// goSourceUnsafeLocalCall reports the unsafe builtins the interpreter answers
// over its own storage, so native dispatch leaves them alone. unsafe.Slice
// over a dependency-indexed pointer stays native (goSourceUnsafeSliceCall).
func (r *Runner) goSourceUnsafeLocalCall(call *syntax.BashPPCall) bool {
	if name, _, ok := r.goSourceUnsafeCallName(call, 2); ok {
		switch name {
		case "Add":
			return true
		case "Slice":
			if address, ok := call.ArgExprs[0].(*syntax.BashPPAddressExpr); ok {
				if index, ok := address.X.(*syntax.BashPPIndexExpr); ok && r.bashPPNativeExpr(index.X) {
					return false
				}
			}
			return true
		}
	}
	if name, _, ok := r.goSourceUnsafeCallName(call, 1); ok {
		return name == "StringData" || name == "SliceData"
	}
	return false
}

// goSourceUnsafeShortDecl binds `x := unsafe.<builtin>(...)` for the builtins
// the interpreter answers over its own storage. The call is evaluated once.
func (r *Runner) goSourceUnsafeShortDecl(d *syntax.BashPPShortDecl) bool {
	if d.Call == nil || len(d.Lhs) != 1 || !r.goSourceUnsafeLocalCall(d.Call) {
		return false
	}
	name := d.Lhs[0].Value
	if value, meta, claimed, err := r.goSourceUnsafeSliceValue(d.Call); claimed {
		if err != nil {
			r.goSourceUnsafeReport(err)
			return true
		}
		if name == "_" {
			return true
		}
		cell := r.goSourceCollectionReadCell(d.Call, value, meta)
		r.bashPPDeclareName(name, cell.vr)
		target := r.bashPPScope.lookup(name)
		target.publish(cell)
		target.object = &bashPPObjectIdentity{owner: name, collection: meta}
		target.valueMeta = meta
		return true
	}
	if name == "_" {
		_, err := r.bashPPPointerExprValue(d.Call)
		if err != nil {
			r.goSourceUnsafeReport(err)
		}
		return true
	}
	return r.bashPPBindPointerExpr(name, d.Call)
}

func (r *Runner) goSourceUnsafeReport(err error) {
	if !errors.Is(err, errBashPPScalarInterrupted) && !r.bashPPPanicking() {
		r.errf("%v\n", err)
		r.exit.code = 2
	}
}

// goSourceUnsafeRead reads a local unsafe builtin call as a value: the slice
// unsafe.Slice aliases, or the pointer unsafe.Add, StringData or SliceData
// names.
func (r *Runner) goSourceUnsafeRead(expr syntax.BashPPExpr) (any, *bashPPCollectionMeta, bool, error) {
	call, ok := bashPPUnparenExpr(expr).(*syntax.BashPPCall)
	if !ok || !r.goSourceUnsafeLocalCall(call) {
		return nil, nil, false, nil
	}
	if value, meta, claimed, err := r.goSourceUnsafeSliceValue(call); claimed {
		return value, meta, true, err
	}
	if ptr, typ, handled, err := r.goSourceUnsafePointerExpr(call); handled {
		return ptr, bashPPPointerMeta(typ), true, err
	}
	ptr, _, err := r.goSourceUnsafeDataPointer(call)
	if err != nil {
		return nil, nil, true, err
	}
	typ, _ := r.goSourceStaticExprType(call)
	if ptr != nil {
		typ = &syntax.BashPPPointerType{Element: ptr.elem}
	}
	return ptr, bashPPPointerMeta(typ), true, nil
}

// goSourceUnsafeDiscardAssign runs `_ = unsafe.<builtin>(...)` for effect.
// unsafe.Slice and unsafe.String make their run-time checks and nothing
// else when the pointer is forged: a discarded result over a forged address
// is never materialised, so no byte behind it is read.
func (r *Runner) goSourceUnsafeDiscardAssign(assign *syntax.BashPPAssign) bool {
	if !r.bashPPGoSource || assign.Call == nil || len(assign.Names) != 1 || assign.Names[0].Value != "_" {
		return false
	}
	name, call, ok := r.goSourceUnsafeCallName(assign.Call, 2)
	var err error
	switch {
	case ok && name == "String":
		_, _, err = r.goSourceUnsafeString(call, true)
	case r.goSourceUnsafeLocalCall(assign.Call):
		if ok && name == "Slice" {
			_, _, _, err = r.goSourceUnsafeSlice(call, true)
		} else {
			_, _, _, err = r.goSourceUnsafeRead(assign.Call)
		}
	default:
		return false
	}
	if err != nil {
		r.goSourceUnsafeReport(err)
	}
	return true
}

// goSourceUnsafePointerCell materializes a conversion to unsafe.Pointer read
// as a value — an interface's dynamic value, a call argument, a collection
// element — as the pointer it retypes, typed unsafe.Pointer. Such a
// conversion has no scalar reading: `unsafe.Pointer(new(T))` and
// `unsafe.Pointer(p)` name storage, not a number.
func (r *Runner) goSourceUnsafePointerCell(conv *syntax.BashPPConvertExpr) (*bashPPCell, bool, error) {
	ptr, target, handled, err := r.goSourceUnsafePointerExpr(conv)
	if !handled || err != nil {
		return nil, handled, err
	}
	cell := bashPPPointerCell(ptr)
	cell.declType, cell.typeName = target, bashPPTypeText(target)
	return cell, true, nil
}
