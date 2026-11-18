// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"errors"
	"fmt"
	"go/constant"
	"go/token"
	"strconv"
	"strings"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func (r *Runner) bashPPUpdate(stmt *syntax.BashPPUpdate) {
	if stmt.Target == nil || stmt.Value == nil {
		r.bashPPUpdateError(stmt.Pos(), "FORM", "unsupported compound assignment form")
		return
	}
	if !bashPPCompoundUpdateOp(stmt.Op.Value) {
		r.bashPPUpdateError(stmt.Op.Pos(), "OP", "unsupported compound assignment operator "+stmt.Op.Value)
		return
	}
	r.bashPPApplyUpdate(stmt.Target, strings.TrimSuffix(stmt.Op.Value, "="), stmt.Value, stmt.Op.Pos())
}

func bashPPCompoundUpdateOp(op string) bool {
	switch op {
	case "+=", "-=", "*=", "/=", "%=", "&=", "|=", "^=", "<<=", ">>=", "&^=":
		return true
	}
	return false
}

func (r *Runner) bashPPApplyUpdate(target syntax.BashPPExpr, op string, rhs syntax.BashPPExpr, pos syntax.Pos) {
	if !r.objectsEnabled() || r.bashPPScope == nil {
		r.bashPPUpdateError(pos, "DISABLED", "compound assignment evaluated with extensions disabled")
		return
	}
	if index, ok := target.(*syntax.BashPPIndexExpr); ok {
		baseType := r.bashPPExprScalarType(index.X)
		if baseType == nil && r.bashPPGoSource {
			// The map operand may itself be a call result, as in `f()[k] += v`
			// (test/fixedbugs/bug196.go). It has no scalar-type cell to consult,
			// so resolve the callee's single result type instead. The map path
			// below reads the operand once, so f is still called exactly once.
			baseType = r.bashPPUpdateCallResultType(index.X)
		}
		if collection, ok := r.bashPPUnderlyingType(baseType).(*syntax.BashPPCollectionType); ok {
			if collection.Kind == "map" {
				r.bashPPApplyMapUpdate(index, collection, op, rhs, pos)
				return
			}
			// A slice is a reference value, so `x[i] += f()` commits to the
			// payload x named when the target was evaluated even if f
			// reassigns x before the store (Go evaluates the operand once).
			// Arrays stay on the variable path below: their assignment
			// copies, so the variable itself is the target's storage.
			if collection.Kind == "slice" && r.bashPPGoSource {
				r.bashPPApplySliceUpdate(index, collection, op, rhs, pos)
				return
			}
		}
	}
	if r.bashPPGoSource && r.goSourceImportedVariablePath(target) {
		ptr, err := r.goSourceNativeAddress(target)
		if err != nil {
			r.bashPPUpdateError(target.Pos(), "TARGET", err.Error())
			return
		}
		curVal, err := r.bashPPNativeAccess(r.ectx, "deref", ptr, "")
		if err != nil {
			if !errors.Is(err, errBashPPScalarInterrupted) {
				r.bashPPUpdateError(target.Pos(), "TARGET", err.Error())
			}
			return
		}
		left, err := curVal.scalar()
		if err != nil {
			r.bashPPUpdateError(target.Pos(), "TYPE", err.Error())
			return
		}
		right, err := r.bashPPEvalScalarExpr(rhs)
		if err != nil {
			if !errors.Is(err, errBashPPScalarInterrupted) {
				r.bashPPUpdateError(rhs.Pos(), "RHS", err.Error())
			}
			return
		}
		value, kind, err := r.bashPPUpdateResult(op, left, right)
		if err != nil {
			if !errors.Is(err, errBashPPScalarInterrupted) {
				r.bashPPUpdateError(pos, "OP", err.Error())
			}
			return
		}
		resultScalar := bashPPScalar{typ: left.typ, runtime: true}
		switch kind {
		case constant.String:
			if s, ok := value.(string); ok {
				resultScalar.value = constant.MakeString(s)
			} else {
				resultScalar.value = constant.MakeString(fmt.Sprint(value))
			}
		case constant.Bool:
			if b, ok := value.(bool); ok {
				resultScalar.value = constant.MakeBool(b)
			} else {
				resultScalar.value = constant.MakeFromLiteral(fmt.Sprint(value), token.INT, 0)
			}
		case constant.Int:
			switch v := value.(type) {
			case int:
				resultScalar.value = constant.MakeInt64(int64(v))
			case string:
				resultScalar.value = constant.MakeFromLiteral(v, token.INT, 0)
			default:
				resultScalar.value = constant.MakeFromLiteral(fmt.Sprint(v), token.INT, 0)
			}
		case constant.Float:
			switch v := value.(type) {
			case float64:
				resultScalar.value = constant.MakeFloat64(v)
			case string:
				resultScalar.value = constant.MakeFromLiteral(v, token.FLOAT, 0)
			default:
				resultScalar.value = constant.MakeFromLiteral(fmt.Sprint(v), token.FLOAT, 0)
			}
		case constant.Complex:
			text := fmt.Sprint(value)
			if parsed := bashPPParseComplex(text); parsed != nil && parsed.Kind() == constant.Complex {
				resultScalar.value = parsed
			} else {
				resultScalar.value = constant.MakeFromLiteral(text, token.FLOAT, 0)
			}
		default:
			r.bashPPUpdateError(target.Pos(), "TYPE", fmt.Sprintf("unsupported result kind %v", kind))
			return
		}
		if resultScalar.value == nil || resultScalar.value.Kind() == constant.Unknown {
			r.bashPPUpdateError(target.Pos(), "TYPE", "result is not scalar")
			return
		}
		bridge, err := bridgeScalar(resultScalar)
		if err != nil {
			r.bashPPUpdateError(target.Pos(), "WRITE", err.Error())
			return
		}
		if _, err := r.bashPPNativeAccess(r.ectx, "deref-set", ptr, "", bridge); err != nil {
			if !errors.Is(err, errBashPPScalarInterrupted) {
				r.bashPPUpdateError(target.Pos(), "WRITE", err.Error())
			}
			return
		}
		r.exit.clear()
		return
	}
	ptr, err := r.bashPPAddress(target)
	if err != nil {
		r.bashPPUpdateError(target.Pos(), "TARGET", err.Error())
		return
	}
	if ptr == nil {
		r.bashPPUpdateError(target.Pos(), "TARGET", "nil assignment target")
		return
	}
	current, meta, expected, err := ptr.read()
	if err != nil || meta != nil && !bashPPScalarMetaKind(meta) {
		if err == nil {
			err = fmt.Errorf("target is not scalar")
		}
		r.bashPPUpdateError(target.Pos(), "TYPE", err.Error())
		return
	}
	var left bashPPScalar
	// The carrier test and the scalar read must agree about which store they
	// saw, so both run off one snapshot of a shared cell; see
	// bashpp_cell_share.go.
	targetCell := ptr.target.view()
	if len(ptr.path) == 0 && !targetCell.pointer && targetCell.vr.Kind == expand.String {
		left = r.bashPPScalarFromCell(targetCell)
	} else {
		left, err = r.bashPPUpdateScalar(current, expected)
	}
	if err != nil {
		r.bashPPUpdateError(target.Pos(), "TYPE", err.Error())
		return
	}
	right, err := r.bashPPEvalScalarExpr(rhs)
	if err != nil {
		if !errors.Is(err, errBashPPScalarInterrupted) {
			r.bashPPUpdateError(rhs.Pos(), "RHS", err.Error())
		}
		return
	}
	value, kind, err := r.bashPPUpdateResult(op, left, right)
	if err != nil {
		// A runtime fault raised while computing the result (integer divide or
		// remainder by zero under GoSource) has already started a recoverable
		// panic and handed back the interrupted sentinel. Propagate it so a
		// deferred recover sees it and the target stays unmodified, rather than
		// reporting a spurious OP diagnostic.
		if !errors.Is(err, errBashPPScalarInterrupted) {
			r.bashPPUpdateError(pos, "OP", err.Error())
		}
		return
	}
	if err := r.bashPPWriteUpdatePointer(ptr, value, kind); err != nil {
		r.bashPPUpdateError(target.Pos(), "WRITE", err.Error())
		return
	}
	r.exit.clear()
}

// A slice index commits to the payload the target names at evaluation time:
// the collection and index are read once, the RHS runs after, and the store
// lands in that payload even if the RHS rebinds the variable that named it.
func (r *Runner) bashPPApplySliceUpdate(target *syntax.BashPPIndexExpr, collection *syntax.BashPPCollectionType, op string, rhs syntax.BashPPExpr, pos syntax.Pos) {
	if root, ok := bashPPCollectionRoot(target); ok {
		if cell := r.bashPPScope.lookup(root).view(); cell != nil {
			if cell.constant || cell.vr.ReadOnly || cell.object != nil && cell.object.readonly {
				r.bashPPUpdateError(target.Pos(), "WRITE", "BASHPP-EREADONLY-MUTATION: cannot mutate readonly slice")
				return
			}
		}
	}
	parent, meta, err := r.bashPPReadExpr(target.X)
	if err != nil {
		r.bashPPUpdateError(target.Pos(), "TARGET", err.Error())
		return
	}
	elements, ok := parent.([]any)
	if !ok || meta == nil || meta.kind != "slice" {
		r.bashPPUpdateError(target.Pos(), "TYPE", "target is not slice storage")
		return
	}
	index, err := r.bashPPCollectionIndex(target.Index)
	if err != nil {
		r.bashPPUpdateError(target.Index.Pos(), "TARGET", err.Error())
		return
	}
	if index.outOfBounds(len(elements)) {
		if r.bashPPGoSource {
			r.bashPPSprint162CollectionBoundsPanic(target, index, len(elements))
			return
		}
		r.bashPPUpdateError(target.Index.Pos(), "TARGET", fmt.Sprintf("BASHPP-ECOLLECTION-BOUNDS: index %s out of bounds for length %d", index.text, len(elements)))
		return
	}
	i := index.value
	if i < len(meta.sequence) && meta.sequence[i] != nil {
		r.bashPPUpdateError(target.Pos(), "TYPE", "target is not scalar")
		return
	}
	left, err := r.bashPPUpdateScalar(elements[i], collection.Element)
	if err != nil {
		r.bashPPUpdateError(target.Pos(), "TYPE", err.Error())
		return
	}
	right, err := r.bashPPEvalScalarExpr(rhs)
	if err != nil {
		if !errors.Is(err, errBashPPScalarInterrupted) {
			r.bashPPUpdateError(rhs.Pos(), "RHS", err.Error())
		}
		return
	}
	value, _, err := r.bashPPUpdateResult(op, left, right)
	if err != nil {
		// See bashPPApplyUpdate: an in-flight divide/remainder-by-zero panic
		// must reach a deferred recover and leave the slice element untouched.
		if !errors.Is(err, errBashPPScalarInterrupted) {
			r.bashPPUpdateError(pos, "OP", err.Error())
		}
		return
	}
	elements[i] = value
	r.exit.clear()
}

// bashPPUpdateCallResultType resolves the single result type of a plain named
// call so a map returned by a function can still route to the read-modify-write
// path. It is deliberately narrow: it never resolves a computed callee or a
// generic function, so it cannot evaluate an argument or call anything — the
// operand's own single evaluation stays in bashPPApplyMapUpdate.
func (r *Runner) bashPPUpdateCallResultType(x syntax.BashPPExpr) syntax.BashPPTypeExpr {
	if paren, ok := x.(*syntax.BashPPParenExpr); ok {
		return r.bashPPUpdateCallResultType(paren.X)
	}
	call, ok := x.(*syntax.BashPPCall)
	if !ok || call.CalleeExpr != nil || call.FuncLit != nil || len(call.Fun) != 1 {
		return nil
	}
	fn := r.bashPPFuncs[call.Fun[0].Value]
	if fn == nil || len(fn.typeParams()) != 0 {
		return nil
	}
	results := bashppResultTypeExprs(fn.results())
	if len(results) != 1 {
		return nil
	}
	return results[0]
}

// Map indices are the one Go assignment target which is assignable but not
// addressable. Evaluate the map and key once, retain the candidate until the
// operation succeeds, and only then commit it to the map slot.
func (r *Runner) bashPPApplyMapUpdate(target *syntax.BashPPIndexExpr, collection *syntax.BashPPCollectionType, op string, rhs syntax.BashPPExpr, pos syntax.Pos) {
	// A map is a reference value, so a map operand with no assignable root --
	// `f()[k] += v` -- is still a legal update target: it is read once below
	// and mutated in place. The readonly guard only applies when the operand
	// names a variable that could itself be readonly.
	if root, ok := bashPPCollectionRoot(target); ok {
		cell := r.bashPPScope.lookup(root).view()
		if cell == nil {
			r.bashPPUpdateError(target.Pos(), "TARGET", "undefined map target "+root)
			return
		}
		if cell.constant || cell.vr.ReadOnly || cell.object != nil && cell.object.readonly {
			r.bashPPUpdateError(target.Pos(), "WRITE", "BASHPP-EREADONLY-MUTATION: cannot mutate readonly map")
			return
		}
	}
	parent, meta, err := r.bashPPReadExpr(target.X)
	if err != nil {
		r.bashPPUpdateError(target.Pos(), "TARGET", err.Error())
		return
	}
	if parent == nil {
		if r.bashPPGoSource {
			r.goSourceRuntimePanic("assignment to entry in nil map")
			return
		}
		r.bashPPUpdateError(target.Pos(), "WRITE", "BASHPP-ENIL-MAP: assignment to nil map")
		return
	}
	mapping, ok := parent.(map[string]any)
	if !ok || meta == nil || meta.kind != "map" {
		r.bashPPUpdateError(target.Pos(), "TYPE", "target is not map storage")
		return
	}
	key, keyMeta, err := r.bashPPEvalElement(target.Index, collection.Key)
	if err != nil {
		if !r.bashPPGoSource || !errors.Is(err, errBashPPScalarInterrupted) {
			r.bashPPUpdateError(target.Index.Pos(), "TARGET", err.Error())
		}
		return
	}
	storage, _, found, err := r.bashPPSprint165MapLookup(meta, key, keyMeta, collection.Key)
	if err != nil {
		// Hashing an unhashable key has already raised its recoverable panic;
		// reporting it again here would print past the recover.
		if !r.bashPPGoSource || !errors.Is(err, errBashPPScalarInterrupted) {
			r.bashPPUpdateError(target.Index.Pos(), "TARGET", err.Error())
		}
		return
	}
	current, child := bashPPSprint165MapEntryValueFound(mapping, meta, storage, found)
	if !found {
		current, child = r.bashPPZeroValue(collection.Element)
	}
	if child != nil {
		r.bashPPUpdateError(target.Pos(), "TYPE", "target is not scalar")
		return
	}
	left, err := r.bashPPUpdateScalar(current, collection.Element)
	if err != nil {
		r.bashPPUpdateError(target.Pos(), "TYPE", err.Error())
		return
	}
	right, err := r.bashPPEvalScalarExpr(rhs)
	if err != nil {
		if !errors.Is(err, errBashPPScalarInterrupted) {
			r.bashPPUpdateError(rhs.Pos(), "RHS", err.Error())
		}
		return
	}
	value, _, err := r.bashPPUpdateResult(op, left, right)
	if err != nil {
		// See bashPPApplyUpdate: an in-flight divide/remainder-by-zero panic must
		// reach a deferred recover and leave the map slot uninserted (test/
		// fixedbugs/issue22881.go: the RHS panics before the map insert starts).
		if !errors.Is(err, errBashPPScalarInterrupted) {
			r.bashPPUpdateError(pos, "OP", err.Error())
		}
		return
	}
	if _, err := r.bashPPSprint165MapStore(mapping, meta, key, keyMeta, collection.Key, value, nil); err != nil {
		if !r.bashPPGoSource || !errors.Is(err, errBashPPScalarInterrupted) {
			r.bashPPUpdateError(target.Pos(), "WRITE", err.Error())
		}
		return
	}
	r.exit.clear()
}

func (r *Runner) bashPPUpdateResult(op string, left, right bashPPScalar) (any, constant.Kind, error) {
	if op == "/" && left.runtime && left.value.Kind() == constant.Float &&
		(right.value.Kind() == constant.Int || right.value.Kind() == constant.Float) && constant.Sign(right.value) == 0 {
		compatible := left.typ == "" && right.typ == "" || left.typ != "" && (right.typ == "" || right.typ == left.typ)
		if compatible && left.typ != "" {
			underlying, ok := r.bashPPUnderlyingTypeName(left.typ)
			compatible = ok && (underlying == "float32" || underlying == "float64")
			if compatible && right.typ == "" {
				compatible = r.bashPPValidateUntypedScalarOperand(right.value, left.typ) == nil
			}
		}
		if compatible {
			return nil, constant.Unknown, fmt.Errorf("BASHPP-EUPDATE-NONFINITE: runtime floating-point division by zero is unsupported by the scalar carrier")
		}
	}
	result, err := r.bashPPBinaryScalar(bashPPOpToken(op), left, right)
	if err != nil {
		return nil, constant.Unknown, err
	}
	// A runtime complex result, and a float result that overflowed to an
	// infinity or NaN, is authoritative in its IEEE carrier: the exact
	// constant is absent (non-finite) or cannot hold a signed zero component.
	// A complex commits its Go spelling, the form collection storage and
	// cells already use for it; a non-finite float commits its float64.
	if result.hasNonFiniteComplex {
		return bashPPScalarStorageString(result), constant.Complex, nil
	}
	if result.hasNonFinite {
		return result.nonFinite, constant.Float, nil
	}
	if result.value == nil {
		return nil, constant.Unknown, fmt.Errorf("BASHPP-EEXPR-OPERAND: operation %s produced no value", op)
	}
	return bashPPUpdateGoValue(result.value), result.value.Kind(), nil
}

func (r *Runner) bashPPUpdateScalar(value any, typ syntax.BashPPTypeExpr) (bashPPScalar, error) {
	out := bashPPScalar{typ: bashPPTypeText(typ), runtime: true}
	if named, ok := r.bashPPUnderlyingType(typ).(*syntax.BashPPNamedType); ok {
		text := fmt.Sprint(value)
		switch {
		case named.Name.Value == "string":
			out.value = constant.MakeString(text)
			return out, nil
		case named.Name.Value == "bool":
			if boolean, ok := value.(bool); ok {
				out.value = constant.MakeBool(boolean)
				return out, nil
			}
		case bashPPIntegerType(named.Name.Value):
			out.value = constant.MakeFromLiteral(text, token.INT, 0)
			if out.value.Kind() == constant.Int {
				return out, nil
			}
		case named.Name.Value == "float32" || named.Name.Value == "float64":
			if special, ok := bashPPNonFiniteText(text); ok && r.bashPPGoSource {
				return bashPPNonFiniteScalar(special, out.typ), nil
			}
			out.value = constant.MakeFromLiteral(text, token.FLOAT, 0)
			if out.value.Kind() == constant.Float {
				return out, nil
			}
		case (named.Name.Value == "complex64" || named.Name.Value == "complex128") && r.bashPPGoSource:
			// A complex target's storage holds either a Go complex value or
			// its Go spelling; both rebuild the IEEE carrier the runtime
			// complex operators consume (signed zeros and infinities intact).
			bits := 128
			if named.Name.Value == "complex64" {
				bits = 64
			}
			switch z := value.(type) {
			case complex64:
				return bashPPNonFiniteComplexScalar(complex128(z), out.typ), nil
			case complex128:
				return bashPPNonFiniteComplexScalar(z, out.typ), nil
			case string:
				if parsed, err := strconv.ParseComplex(z, bits); err == nil {
					return bashPPNonFiniteComplexScalar(parsed, out.typ), nil
				}
			}
		}
	}
	switch value := value.(type) {
	case string:
		out.value = constant.MakeString(value)
	case bool:
		out.value = constant.MakeBool(value)
	case int:
		out.value = constant.MakeInt64(int64(value))
	case float64:
		out.value = constant.MakeFloat64(value)
	default:
		return bashPPScalar{}, fmt.Errorf("value of type %T is not scalar", value)
	}
	return out, nil
}

func bashPPUpdateGoValue(value constant.Value) any {
	switch value.Kind() {
	case constant.String:
		return constant.StringVal(value)
	case constant.Bool:
		return constant.BoolVal(value)
	case constant.Int:
		if i, ok := constant.Int64Val(value); ok {
			return int(i)
		}
		return value.ExactString()
	case constant.Float:
		f, _ := constant.Float64Val(value)
		return f
	}
	return bashPPScalarString(value)
}

func (r *Runner) bashPPWriteUpdatePointer(ptr *bashPPPointer, value any, kind constant.Kind) error {
	target := ptr.target.view()
	if target.object != nil && target.object.readonly {
		return fmt.Errorf("BASHPP-EREADONLY-MUTATION: cannot mutate readonly value %q", target.object.owner)
	}
	if guard := target.view(); guard.constant || guard.vr.ReadOnly {
		return fmt.Errorf("BASHPP-EREADONLY-MUTATION: cannot mutate readonly or constant value")
	}
	if len(ptr.path) == 0 {
		text := fmt.Sprint(value)
		// The whole scalar bundle is published as one, so a concurrent reader
		// of this shared cell sees the value before or after this store and
		// never a mixture of the two; see bashpp_cell_share.go. Nothing but
		// the stores belongs inside the guarded region.
		ptr.target.lock()
		defer ptr.target.unlock()
		ptr.target.vr.Set = true
		ptr.target.vr.Kind = expand.String
		ptr.target.vr.Str = text
		ptr.target.vr.Obj = nil
		ptr.target.vr.List, ptr.target.vr.Map = nil, nil
		ptr.target.vr.ListMap, ptr.target.vr.ListSet = nil, nil
		ptr.target.scalarKind = kind
		// The stored spelling is now the value. Drop any IEEE carrier left
		// by the previous value so it cannot shadow the new spelling; the
		// cell reader rebuilds the carrier from the spelling and the
		// declared type (complex width, float infinities and NaN).
		ptr.target.hasNonFinite, ptr.target.nonFinite = false, 0
		ptr.target.hasNonFiniteComplex, ptr.target.nonFiniteComplex = false, 0
		return nil
	}
	parent, parentMeta, _, err := ptr.readParent()
	if err != nil {
		return err
	}
	last := ptr.path[len(ptr.path)-1]
	if last.field != "" {
		mapping, ok := parent.(map[string]any)
		if !ok {
			return fmt.Errorf("selector target storage is no longer a struct")
		}
		var layout map[string]*bashPPCollectionMeta
		if parentMeta != nil {
			layout = parentMeta.mapping
		}
		bashPPStorageSetField(mapping, layout, last.field, value, nil)
		return nil
	}
	sequence, ok := parent.([]any)
	if !ok || last.index < 0 || last.index >= len(sequence) {
		return fmt.Errorf("index target storage is no longer addressable")
	}
	sequence[last.index] = value
	if parentMeta != nil {
		parentMeta.sequence[last.index] = nil
	}
	return nil
}

func (r *Runner) bashPPUpdateError(pos syntax.Pos, kind, message string) {
	r.errf("%sBASHPP-EUPDATE-%s: %s\n", r.bashErrPrefix(pos), kind, message)
	r.exit = exitStatus{code: 2}
}
