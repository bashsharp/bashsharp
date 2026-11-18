// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"fmt"
	"go/constant"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// bashPPSprint162ComplexCollectionText recognises the interpreter's finite
// complex scalar carrier. Collections remain JSON-shaped, so complex values
// stay in their lossless string spelling and are reconstructed from the
// destination type when read.
func bashPPSprint162ComplexCollectionText(value any) bool {
	text, ok := value.(string)
	if !ok {
		return false
	}
	if _, special := bashPPNonFiniteComplexText(text); special {
		return true
	}
	return bashPPParseComplex(text).Kind() == constant.Complex
}

// bashPPSprint162CollectionBoundsPanic turns a dynamic indexing fault into
// Go's recoverable runtime panic. Static constant bounds remain the checker's
// responsibility and never reach this evaluator path.
func (r *Runner) bashPPSprint162CollectionBoundsPanic(expr syntax.BashPPExpr, index bashPPCollectionIndexValue, length int) error {
	message := fmt.Sprintf("runtime error: index out of range [%s]", index.text)
	// Go's runtime deliberately omits the collection length for a negative
	// index. Keep the exact wide index spelling in both forms; conversion to a
	// host int only happens after this bounds check succeeds.
	if !index.less(bashPPCollectionIndexInt(0)) {
		message += fmt.Sprintf(" with length %d", length)
	}
	pos := syntax.Pos{}
	if expr != nil {
		pos = expr.Pos()
		switch x := expr.(type) {
		case *syntax.BashPPIndexExpr:
			pos = x.Lbrack
		case *syntax.BashPPSliceExpr:
			pos = x.Lbrack
		}
		r.bashPPPanic.traceSource = r.filename
		if r.bashPPGoSourceFile != nil {
			if source, ok := r.bashPPGoSourceFile.SourceAt(pos); ok {
				r.bashPPPanic.traceSource = source.Name
			}
		}
		r.bashPPPanic.traceLine = pos.Line()
		r.bashPPPanic.traceFrames = r.bashPPPanic.traceFrames[:0]
		for _, frame := range r.callStack {
			r.bashPPPanic.traceFrames = append(r.bashPPPanic.traceFrames, frame.funcName)
		}
	}
	saved := r.curStmtPos
	if pos.IsValid() {
		r.curStmtPos = pos
	}
	r.bashPPRaise(message)
	r.curStmtPos = saved
	return errBashPPScalarInterrupted
}

// bashPPSprint162PointerCollectionStorage applies Go's implicit dereference
// for an indexed assignment through a pointer to an array. Pointer-to-slice
// and pointer-to-map values still require an explicit dereference in Go and
// are rejected by the checker before reaching this path.
func (r *Runner) bashPPSprint162PointerCollectionStorage(value any, meta *bashPPCollectionMeta) (any, *bashPPCollectionMeta, error) {
	pointer, ok := value.(*bashPPPointer)
	if !ok {
		return value, meta, nil
	}
	if pointer == nil {
		return nil, nil, fmt.Errorf("BASHPP-ENIL-DEREF: dereference of nil pointer")
	}
	value, meta, _, err := pointer.read()
	return value, meta, err
}

// bashPPSprint162MakeChannelBuiltin routes every Go make(chan T) value through
// the channel allocator, including defined channel types and interpreter-owned
// aggregate element types. The older fast path only claimed raw channel types
// whose elements could live in the dependency process.
func (r *Runner) bashPPSprint162MakeChannelBuiltin(name string, call *syntax.BashPPCall) (*bashPPCell, bool) {
	if !r.bashPPGoSource || name != "make" || call == nil {
		return nil, false
	}
	if _, ok := r.bashPPUnderlyingType(call.ArgType).(*syntax.BashPPChanType); !ok {
		return nil, false
	}
	var capacity syntax.BashPPExpr
	var word *syntax.Word
	if len(call.ArgExprs) > 1 {
		capacity = call.ArgExprs[1]
	}
	if len(call.Args) > 1 {
		word = call.Args[1]
	}
	cell, err := r.goSourceMakeChannelCell(call.ArgType, capacity, word)
	if err != nil {
		r.goSourceNativeChannelError(err)
		return nil, true
	}
	return cell, true
}

// bashPPSprint162MakeSlicePanic applies Go's runtime failure for dynamic make
// sizes. Constant-invalid sizes remain checker errors and Classic Bash++ keeps
// its established builtin diagnostic.
func (r *Runner) bashPPSprint162MakeSlicePanic(length, capacity int) bool {
	if !r.bashPPGoSource {
		return false
	}
	message := "runtime error: makeslice: len out of range"
	if length >= 0 && capacity < length {
		message = "runtime error: makeslice: cap out of range"
	}
	r.bashPPRaiseRuntimeError("runtime.errorString", message)
	return true
}

// bashPPCollectionBridgeValue validates a typed value which crossed
// the dependency boundary in its transport wrapper. Handles stay in the
// authenticated dependency session after the helper verifies assignability;
// non-finite floats stay wrapped because they cannot live in the collection's
// JSON-shaped scalar payload.
func (r *Runner) bashPPCollectionBridgeValue(value any, expected syntax.BashPPTypeExpr) (any, *bashPPCollectionMeta, bool, error) {
	native, ok := value.(*bashPPBridgeValue)
	if !ok || native == nil {
		return nil, nil, false, nil
	}
	if _, _, scalarErr := bashPPBridgeScalarValue(*native); scalarErr != nil {
		value, meta, err := r.goSourceNativeAssignedValue(*native, expected)
		if err != nil {
			return nil, nil, true, fmt.Errorf("BASHPP-ECOLLECTION-ELEMENT: %v", err)
		}
		meta.kind = "native"
		return value, meta, true, nil
	}
	actual := bashPPBridgeDynamicType(native.Type)
	if native.Type == "" || !r.bashPPTypeAssignable(actual, expected) {
		return nil, nil, true, fmt.Errorf("BASHPP-ECOLLECTION-ELEMENT: cannot use %s as %s", bashPPTypeText(actual), bashPPTypeText(expected))
	}
	_, meta, err := bashPPBridgeScalarValue(*native)
	if err != nil {
		return nil, nil, true, fmt.Errorf("BASHPP-ECOLLECTION-ELEMENT: %v", err)
	}
	if meta != nil {
		return nil, nil, true, fmt.Errorf("BASHPP-ECOLLECTION-ELEMENT: dependency scalar carried collection metadata")
	}
	return native, &bashPPCollectionMeta{kind: "bridge-scalar", typ: expected}, true, nil
}
