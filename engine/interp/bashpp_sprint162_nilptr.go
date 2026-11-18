// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"context"
	"errors"
	"go/token"
	"strings"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// A nil dereference is a run-time fault, not a static refusal.
//
// Go detects a nil pointer dereference when the load or store executes and
// raises a runtime.Error ("runtime error: invalid memory address or nil
// pointer dereference") that unwinds like any other panic: deferred calls
// run, a directly deferred recover stops it, and an unrecovered one
// terminates the program with status 2. The evaluator used to detect the
// same fault and report it as a BASHPP-ENIL-DEREF refusal, which no recover
// could observe. Every detection site now hands back a [bashPPRuntimeError]
// and the expression entry points convert it into the panic Go raises.
//
// Classic Bash++ keeps its refusal wording: the error's text is unchanged
// and only Go source turns it into a panic, so scripts that never spell a
// defer or recover see exactly what they saw before.

// bashPPRuntimeError is a Go run-time panic detected during evaluation. Its
// Error text is the evaluator's refusal wording, which classic Bash++
// reports as before; runtime is the runtime.Error message Go raises.
type bashPPRuntimeError struct {
	refusal string
	runtime string
}

func (e *bashPPRuntimeError) Error() string { return e.refusal }

// bashPPRuntimeErrorText is the text of the runtime.Error Go's runtime
// reports for the fault, and therefore the value a recover observes.
func (e *bashPPRuntimeError) bashPPRuntimeErrorText() string {
	// The runtime raises a few faults as plainError, whose text carries no
	// prefix; bashPPRuntimeErrorPayload boxes them as the same value.
	if bashPPRuntimePlainErrors[e.runtime] {
		return e.runtime
	}
	return "runtime error: " + e.runtime
}

// errBashPPNilDereference is the fault every nil dereference site reports.
// It is a single value so that the plain-function sites, which have no
// runner to raise through, report the same fault as the runner's own.
var errBashPPNilDereference = &bashPPRuntimeError{
	refusal: "BASHPP-ENIL-DEREF: dereference of nil pointer",
	runtime: "invalid memory address or nil pointer dereference",
}

// errBashPPNilEmbeddedDereference is the same fault reached through a nil
// embedded pointer while a promoted field or method is resolved.
var errBashPPNilEmbeddedDereference = &bashPPRuntimeError{
	refusal: "BASHPP-ENIL-DEREF: dereference of nil embedded pointer",
	runtime: "invalid memory address or nil pointer dereference",
}

// errBashPPNilMapAssign is the fault every nil-map element write reports:
// Go raises the plainError `assignment to entry in nil map`, which a defer
// in the same frame recovers after the assignments before it took effect.
var errBashPPNilMapAssign = &bashPPRuntimeError{
	refusal: "BASHPP-ENIL-MAP: assignment to nil map",
	runtime: "assignment to entry in nil map",
}

// goSourceRuntimeFault turns a run-time fault into the panic Go raises.
//
// Any other error, and every error in classic Bash++, is returned unchanged.
// For a fault in Go source the panic is raised here, once, and the
// interrupted sentinel is returned in its place so the statement that was
// evaluating the expression halts without reporting a second diagnostic —
// the same contract goSourceInterfaceEqual established for an uncomparable
// dynamic type.
//
// Raising is guarded against a panic already unwinding the same statement:
// several evaluation paths retry an expression through a fallback reader
// after the first reader fails, and the retry must not raise the fault a
// second time. A deferred call running for that panic is not a retry, so a
// fault raised inside one starts the new unwind Go would start.
func (r *Runner) goSourceRuntimeFault(err error) error {
	if err == nil || !r.bashPPGoSource {
		return err
	}
	var fault *bashPPRuntimeError
	if !errors.As(err, &fault) {
		return err
	}
	if r.bashPPPanicHalts() {
		return errBashPPScalarInterrupted
	}
	r.goSourceRuntimePanic(fault.bashPPRuntimeErrorText())
	return errBashPPScalarInterrupted
}

// goSourceRuntimePanic raises a runtime.Error panic at the statement that
// is executing, retaining the interpreted frames as an explicit panic call
// would, so an unrecovered fault reports the same Go-shaped traceback.
func (r *Runner) goSourceRuntimePanic(text string) {
	r.goSourceRuntimePanicValue(text, nil)
}

func (r *Runner) goSourceRuntimePanicValue(text string, value any) {
	r.bashPPPanic.traceSource = r.filename
	r.bashPPPanic.traceLine = r.curStmtPos.Line()
	r.bashPPPanic.traceFrames = r.bashPPPanic.traceFrames[:0]
	for _, frame := range r.callStack {
		r.bashPPPanic.traceFrames = append(r.bashPPPanic.traceFrames, frame.funcName)
	}
	if value != nil {
		r.bashPPRaiseValue(text, value)
		return
	}
	r.bashPPRaise(text)
}

// bashPPReportNilDereference is the statement-level form of the fault, for
// the assignment and declaration paths that report their own diagnostics
// instead of returning an error.
func (r *Runner) bashPPReportNilDereference() {
	if err := r.goSourceRuntimeFault(errBashPPNilDereference); errors.Is(err, errBashPPScalarInterrupted) {
		return
	}
	r.errf("%v\n", errBashPPNilDereference)
	r.exit = exitStatus{code: 2}
}

// goSourceRangePointerArray ranges over a pointer to an array, which Go
// permits with the array's own iteration: `for i, v := range p` reads p's
// elements in place. Two details are the whole reason it is its own path:
//
//   - The elements are read through the pointer at each iteration rather
//     than from a copy, since ranging a pointer never copies the array.
//   - With no non-blank value iteration variable, len(*p) is a constant and
//     the spec says the range expression is not evaluated at all, so a nil p
//     ranges its indices without faulting. With one, *p is evaluated and a
//     nil p is the nil dereference it always was.
//
// It reports whether the operand was such a pointer; anything else is left
// to the collection ranges.
func (r *Runner) goSourceRangePointerArray(ctx context.Context, rng *syntax.BashPPRange, value any, meta *bashPPCollectionMeta) bool {
	if !r.bashPPGoSource || meta == nil || meta.kind != "pointer" {
		return false
	}
	pointerType, ok := r.bashPPPointerType(meta.typ)
	if !ok {
		return false
	}
	array, ok := r.bashPPUnderlyingType(pointerType.Element).(*syntax.BashPPCollectionType)
	if !ok || array.Kind != "array" || array.Length == nil {
		return false
	}
	ptr, _ := value.(*bashPPPointer)
	if ptr == nil {
		// A second iteration variable that is not blank demands the elements,
		// so a nil p is the dereference fault it always was. With the value
		// blank (or absent) no element is ever read: len(*p) is a constant and
		// the spec says the range expression is not evaluated, so the loop
		// still walks the array's fixed index length over a nil pointer.
		if len(rng.Names) > 1 && rng.Names[1].Value != "_" {
			r.goSourceRuntimeFault(errBashPPNilDereference)
			return true
		}
		length, err := r.bashPPArrayLength(array.Length.Value)
		if err != nil {
			r.bashPPRangeError(rng, "BASHPP-ERANGE-TYPE: cannot range over %s: %v", bashPPTypeText(meta.typ), err)
			return true
		}
		for i := 0; i < length; i++ {
			if !r.bashPPRangeIteration(ctx, rng, i, bashPPRangeNamedType("int"), nil, nil, array.Element) {
				return true
			}
		}
		return true
	}
	target, targetMeta, _, err := ptr.read()
	if err != nil {
		if err = r.goSourceRuntimeFault(err); !errors.Is(err, errBashPPScalarInterrupted) {
			r.bashPPRangeError(rng, "%v", err)
		}
		return true
	}
	target, targetMeta, err = r.bashPPSliceArrayPointerValue(ptr, target, targetMeta)
	if err != nil {
		if err = r.goSourceRuntimeFault(err); !errors.Is(err, errBashPPScalarInterrupted) {
			r.bashPPRangeError(rng, "%v", err)
		}
		return true
	}
	sequence, _ := target.([]any)
	for i := 0; i < len(sequence); i++ {
		var elemMeta *bashPPCollectionMeta
		if targetMeta != nil && i < len(targetMeta.sequence) {
			elemMeta = targetMeta.sequence[i]
		}
		if !r.bashPPRangeIteration(ctx, rng, i, bashPPRangeNamedType("int"), sequence[i], elemMeta, array.Element) {
			return true
		}
	}
	return true
}

// nil as a first-class value.
//
// The scalar evaluator refuses the identifier nil, correctly: nil is not a
// scalar. It is a value of every pointer, slice, map, chan, func and
// interface type, and it is compared, switched on and passed as such. The
// paths below give the shapes the scalar path rejected their Go meaning.

// goSourceFuncComparable is the comparable form of a function value named by
// an identifier: a closure held in a variable, or a declared function. Both
// are non-nil func values; only a func variable holding no closure is nil.
func (r *Runner) goSourceFuncComparable(name string) (bashPPComparableValue, bool) {
	if !r.bashPPGoSource {
		return bashPPComparableValue{}, false
	}
	cell := r.bashPPScope.lookup(name).view()
	if cell == nil {
		if fn := r.bashPPFuncs[name]; fn != nil {
			typ := &syntax.BashPPFuncType{Params: fn.params(), Results: fn.results()}
			return bashPPComparableValue{value: name, meta: &bashPPCollectionMeta{kind: "func", typ: typ}}, true
		}
		return bashPPComparableValue{}, false
	}
	if _, closure := r.bashPPClosure(cell.vr.Str); closure {
		return bashPPComparableValue{value: cell.vr.Str, meta: &bashPPCollectionMeta{kind: "func", typ: cell.declType}}, true
	}
	return bashPPComparableValue{}, false
}

// goSourceTypedNilComparable is the comparable form of a typed nil spelled as
// a conversion — `[]T(nil)`, `(map[K]V)(nil)`, `(func())(nil)` — for the
// types whose zero value is nil and which the pointer and interface
// conversion paths do not already cover.
func (r *Runner) goSourceTypedNilComparable(x *syntax.BashPPConvertExpr) (bashPPComparableValue, bool) {
	if !r.bashPPGoSource || !goSourceNilLiteral(x.X) {
		return bashPPComparableValue{}, false
	}
	target := r.bashPPConvertTarget(x)
	if target == nil || !r.goSourceNilableType(target) {
		return bashPPComparableValue{}, false
	}
	if _, iface := r.bashPPInterfaceType(target); iface {
		cell, handled, err := r.goSourceNilValueCell(x)
		if err != nil || !handled || cell == nil {
			return bashPPComparableValue{}, false
		}
		return bashPPComparableValue{value: cell.interfaceValue, meta: &bashPPCollectionMeta{kind: "interface", typ: cell.declType}}, true
	}
	switch r.bashPPUnderlyingType(target).(type) {
	case *syntax.BashPPFuncType:
		return bashPPComparableValue{meta: &bashPPCollectionMeta{kind: "func", typ: target}}, true
	case *syntax.BashPPChanType:
		return bashPPComparableValue{meta: &bashPPCollectionMeta{kind: "channel", typ: target}}, true
	case *syntax.BashPPCollectionType:
		_, meta := r.bashPPZeroValue(target)
		if meta == nil {
			return bashPPComparableValue{}, false
		}
		return bashPPComparableValue{meta: meta}, true
	}
	return bashPPComparableValue{}, false
}

// goSourceValueSwitchTag reports whether a switch tag is a value the scalar
// switch cannot hold — a func, map, slice, chan, pointer or interface — so
// that its cases are decided by Go's comparison of those values instead.
// Only tags whose re-evaluation per case is free of effects qualify: a
// name, a typed nil or a conversion of one, an address.
func (r *Runner) goSourceValueSwitchTag(expr syntax.BashPPExpr) bool {
	if !r.bashPPGoSource || expr == nil {
		return false
	}
	switch x := expr.(type) {
	case *syntax.BashPPParenExpr:
		return r.goSourceValueSwitchTag(x.X)
	case *syntax.BashPPIdent:
		if x.Name.Value == "nil" {
			return true
		}
		cell := r.bashPPScope.lookup(x.Name.Value).view()
		if cell == nil {
			return r.bashPPFuncs[x.Name.Value] != nil
		}
		if cell.pointer || cell.interfaceValue != nil || cell.vr.Kind == expand.Object {
			return true
		}
		if _, closure := r.bashPPClosure(cell.vr.Str); closure {
			return true
		}
		return r.goSourceNilableType(cell.declType)
	case *syntax.BashPPConvertExpr:
		if !goSourceNilLiteral(x.X) {
			return false
		}
		target := r.bashPPConvertTarget(x)
		return target != nil && r.goSourceNilableType(target)
	case *syntax.BashPPAddressExpr:
		return true
	case *syntax.BashPPCompositeLit:
		return x.LitType != nil && !r.bashPPNativeType(x.LitType)
	case *syntax.BashPPDerefExpr:
		// A dereference of a pointer to a func type is a function value,
		// which the scalar tag path cannot carry; the value switch
		// compares it through the same func metadata a variable uses.
		// Other dereferences keep their existing tag routing.
		if pointer, ok := r.bashPPUnderlyingType(r.bashPPExprScalarType(x.X)).(*syntax.BashPPPointerType); ok {
			if _, ok := r.bashPPUnderlyingType(pointer.Element).(*syntax.BashPPFuncType); ok {
				return true
			}
		}
		return false
	}
	return false
}

// goSourceValueSwitch selects the arm of a switch on a non-scalar tag. Each
// case is Go's `tag == case` comparison, in source order, and the first true
// one is selected; with none, the default arm. It returns the arm index or
// -1, and whether a diagnostic already stopped the statement.
func (r *Runner) goSourceValueSwitch(sw *syntax.BashPPSwitch) (int, bool) {
	tag, err := r.bashPPComparableExpr(sw.Tag)
	if err != nil {
		if !errors.Is(err, errBashPPScalarInterrupted) {
			r.errf("%s%v\n", r.bashErrPrefix(sw.Tag.Pos()), err)
			r.exit = exitStatus{code: 2}
		}
		return -1, true
	}
	defaultArm := -1
	for armIndex, arm := range sw.Arms {
		if len(arm.Exprs) == 0 {
			defaultArm = armIndex
			continue
		}
		for _, expr := range arm.Exprs {
			match, err := r.goSourceValueSwitchCase(tag, expr)
			if err != nil && bashPPComparableFallback(err) {
				match, err = r.goSourceValueSwitchScalarCase(tag, expr)
			}
			if err != nil {
				if !errors.Is(err, errBashPPScalarInterrupted) {
					r.errf("%s%v\n", r.bashErrPrefix(expr.Pos()), err)
					r.exit = exitStatus{code: 2}
				}
				return -1, true
			}
			if match {
				return armIndex, false
			}
		}
	}
	return defaultArm, false
}

func (r *Runner) goSourceValueSwitchCase(tag bashPPComparableValue, expr syntax.BashPPExpr) (bool, error) {
	candidate, err := r.bashPPComparableExpr(expr)
	if err != nil {
		return false, err
	}
	return r.bashPPCompareComparableValues(tag, token.EQL, candidate)
}

// goSourceTypedNilBridgeValue is the native form of a typed nil spelled as a
// conversion — `(func())(nil)`, `(*T)(nil)`, `[]byte(nil)` — when it is
// handed to a dependency: a nil of that type, carried as the typed nil the
// bridge already transports for a nil callback.
func (r *Runner) goSourceTypedNilBridgeValue(expr syntax.BashPPExpr) (bashPPBridgeValue, bool, error) {
	if !r.bashPPGoSource {
		return bashPPBridgeValue{}, false, nil
	}
	for {
		paren, ok := expr.(*syntax.BashPPParenExpr)
		if !ok {
			break
		}
		expr = paren.X
	}
	conversion, ok := expr.(*syntax.BashPPConvertExpr)
	if !ok || !goSourceNilLiteral(conversion.X) {
		return bashPPBridgeValue{}, false, nil
	}
	target := r.bashPPConvertTarget(conversion)
	if target == nil || !r.goSourceNilableType(target) {
		return bashPPBridgeValue{}, false, nil
	}
	if r.goSourceUnsafePointerType(target) {
		value, err := r.bashPPNativeTypeRequest("new", target)
		return value, true, err
	}
	return bashPPBridgeValue{Kind: "nil", Type: r.bashPPBridgeTypeIdentity(r.bashPPCanonicalAssignableType(target))}, true, nil
}

// goSourceNilInterfaceSource is the source cell of the untyped nil an
// interface conversion or assignment is given: the nil interface itself.
func (r *Runner) goSourceNilInterfaceSource(expr syntax.BashPPExpr) (*bashPPCell, bool) {
	if !r.bashPPGoSource || !goSourceNilLiteral(expr) || r.bashPPScope.lookup("nil") != nil {
		return nil, false
	}
	cell, _, err := r.goSourceNilValueCell(expr)
	if err != nil || cell == nil {
		return nil, false
	}
	return cell, true
}

// goSourceValueSwitchScalarCase compares a non-scalar tag with a scalar case
// — `switch any(nil) { case int(0): }` — where the case has no comparable
// spelling of its own. An interface tag compares by dynamic type and value,
// as Go does; any other tag has no scalar equal and never matches. The case's
// own declared type travels with its value: boxing it against the interface
// needs the type the case is written at, not the default type of the constant
// it renders to, or `case A(1)` would box as int and so both miss an interface
// holding an A and match one holding a B.
func (r *Runner) goSourceValueSwitchScalarCase(tv bashPPComparableValue, expr syntax.BashPPExpr) (bool, error) {
	scalar, err := r.bashPPEvalScalarExpr(expr)
	if err != nil {
		return false, err
	}
	cv := bashPPComparableValue{value: bashPPScalarAny(scalar.value), scalarType: r.bashPPCanonicalScalarType(scalar.typ)}
	tv.value = bashPPComparablePayload(tv.value, tv.meta)
	if equal, handled, err := r.goSourceInterfaceEqual(tv, cv); handled {
		return equal, err
	}
	return false, nil
}

// goSourceNilFuncCallee reports whether a call names a func-typed variable
// that holds no function. Calling it is Go's nil dereference, raised here
// so the call is neither looked up as a declaration nor sent to a dependency.
func (r *Runner) goSourceNilFuncCallee(call *syntax.BashPPCall) bool {
	if !r.bashPPGoSource || call == nil || call.CalleeExpr != nil || call.FuncLit != nil || len(call.Fun) != 1 || r.bashPPScope == nil {
		return false
	}
	// Every test below names another of the cell's fields; one snapshot.
	cell := r.bashPPScope.lookup(call.Fun[0].Value).view()
	if cell == nil || cell.pointer || cell.interfaceValue != nil {
		return false
	}
	if native, ok := cell.vr.Obj.(*bashPPBridgeValue); ok && native != nil {
		if native.Kind != "nil" {
			return false
		}
		if _, ok := r.bashPPUnderlyingType(cell.declType).(*syntax.BashPPFuncType); ok {
			return true
		}
		return strings.HasPrefix(native.Type, "func(")
	}
	if cell.vr.Kind == expand.Object {
		return false
	}
	if _, ok := r.bashPPUnderlyingType(cell.declType).(*syntax.BashPPFuncType); !ok {
		return false
	}
	if _, closure := r.bashPPClosure(cell.vr.Str); closure {
		return false
	}
	return cell.vr.Str == "" || cell.vr.Str == "nil"
}

// goSourceNilAssignCandidate is the value `x = nil` stores in an
// interpreter-owned variable: the nil of x's own type — a nil pointer, map,
// slice, func, chan or interface — built the way a nil argument is bound to
// a parameter of that type. Variables the dependency owns keep their native
// nil path; a target whose type has no nil is not this path's, the checker
// having refused the assignment already.
func (r *Runner) goSourceNilAssignCandidate(target *bashPPCell, expr syntax.BashPPExpr) (*bashPPCell, bool, error) {
	if !r.bashPPGoSource || target == nil || !goSourceNilLiteral(expr) || r.bashPPScope.lookup("nil") != nil {
		return nil, false, nil
	}
	typ := target.declType
	if typ == nil {
		// A cell born from `q := []T{...}` has no declared type; its
		// collection metadata carries the type the literal built, and that —
		// not the scalar spelling fallback — is what nil must be typed as.
		if meta := bashPPCellMeta(target); meta != nil && meta.typ != nil {
			typ = meta.typ
		} else {
			typ = bashPPInferredCellType(target)
		}
	}
	if typ == nil || !r.goSourceNilableType(typ) {
		return nil, false, nil
	}
	cell, _, err := r.goSourceNilValueCell(expr)
	if err != nil || cell == nil {
		return nil, false, err
	}
	typed, err := r.goSourceExpectedCell(cell, typ)
	if err != nil {
		return nil, true, err
	}
	return typed, true, nil
}

// goSourceIndexedPointee follows the implicit dereference Go inserts when a
// pointer to an array is indexed or sliced: `p[i]` and `p[lo:hi]` read
// through p. A nil p is the nil dereference; any other value is returned as
// it was.
func (r *Runner) goSourceIndexedPointee(value any, meta *bashPPCollectionMeta) (any, *bashPPCollectionMeta, error) {
	if !r.bashPPGoSource || meta == nil || meta.kind != "pointer" {
		return value, meta, nil
	}
	pointerType, ok := r.bashPPPointerType(meta.typ)
	if !ok {
		return value, meta, nil
	}
	if array, ok := r.bashPPUnderlyingType(pointerType.Element).(*syntax.BashPPCollectionType); !ok || array.Kind != "array" {
		return value, meta, nil
	}
	ptr, _ := value.(*bashPPPointer)
	if ptr == nil {
		return nil, nil, r.goSourceRuntimeFault(errBashPPNilDereference)
	}
	target, targetMeta, _, err := ptr.read()
	if err != nil {
		return nil, nil, r.goSourceRuntimeFault(err)
	}
	return target, targetMeta, nil
}

// goSourceCompositeComparable is the comparable form of a composite literal
// used as an operand. Structs and arrays are compared field by field as Go
// compares them. Slice and map literals also cross this boundary so their
// one legal comparison, against nil, can be decided by the shared aggregate
// comparator; comparisons with any other value remain rejected there.
func (r *Runner) goSourceCompositeComparable(lit *syntax.BashPPCompositeLit) (bashPPComparableValue, bool, error) {
	if !r.bashPPGoSource || lit.LitType == nil {
		return bashPPComparableValue{}, false, nil
	}
	if r.bashPPNativeType(lit.LitType) {
		return bashPPComparableValue{}, false, nil
	}
	value, meta, err := r.bashPPEvalComposite(lit, lit.LitType)
	if err != nil {
		return bashPPComparableValue{}, true, err
	}
	if meta == nil || (meta.kind != "struct" && meta.kind != "array" && meta.kind != "inferred-array" && meta.kind != "slice" && meta.kind != "map") {
		return bashPPComparableValue{}, false, nil
	}
	return bashPPComparableValue{value: value, meta: meta}, true, nil
}

// goSourceConvertedInterfaceValue is the interface value an explicit
// interface conversion — `any(new(T))`, `I(v)` — yields when it is stored in
// an interface: the conversion's own dynamic value, which the scalar path
// would otherwise try, and fail, to read as a scalar.
func (r *Runner) goSourceConvertedInterfaceValue(expr syntax.BashPPExpr, iface *syntax.BashPPInterfaceType) (*bashPPInterfaceValue, expand.Variable, bool, error) {
	if !r.bashPPGoSource {
		return nil, expand.Variable{}, false, nil
	}
	cell, handled, err := r.bashPPInterfaceConversion(expr)
	if !handled {
		return nil, expand.Variable{}, false, nil
	}
	if err != nil {
		return nil, expand.Variable{}, true, err
	}
	source := cell.interfaceValue
	if source == nil || source.nilIface {
		return &bashPPInterfaceValue{nilIface: true}, expand.Variable{Set: true, Kind: expand.String}, true, nil
	}
	if err := r.bashPPImplementsCell(source.cell, source.dynamic, iface); err != nil {
		return nil, expand.Variable{}, true, err
	}
	iv := *source
	iv.cell = bashPPCopyInterfaceCell(source.cell)
	return &iv, iv.cell.vr, true, nil
}

// goSourceConvertedComposite is the value of a composite literal converted
// to another struct or array type with the same underlying type —
// `node(SourceRange{})`, `Tbigv([2]uintptr{5, 6})`: the literal's value,
// retyped so the target's methods resolve on it. The checker has already
// established the two types convert; nothing is re-verified here.
func (r *Runner) goSourceConvertedComposite(x *syntax.BashPPConvertExpr) (any, *bashPPCollectionMeta, bool, error) {
	if !r.bashPPGoSource {
		return nil, nil, false, nil
	}
	target := r.bashPPConvertTarget(x)
	if target == nil || r.bashPPNativeType(target) {
		return nil, nil, false, nil
	}
	switch shape := r.bashPPUnderlyingType(target).(type) {
	case *syntax.BashPPStructType:
	case *syntax.BashPPCollectionType:
		if shape.Kind != "array" {
			return nil, nil, false, nil
		}
	default:
		return nil, nil, false, nil
	}
	var value any
	var meta *bashPPCollectionMeta
	var err error
	operand := x.X
	for {
		paren, ok := operand.(*syntax.BashPPParenExpr)
		if !ok {
			break
		}
		operand = paren.X
	}
	if lit, ok := operand.(*syntax.BashPPCompositeLit); ok {
		if lit.LitType == nil || r.bashPPNativeType(lit.LitType) {
			return nil, nil, false, nil
		}
		value, meta, err = r.bashPPEvalComposite(lit, lit.LitType)
	} else {
		value, meta, err = r.bashPPReadExpr(x.X)
	}
	if err != nil {
		return nil, nil, true, err
	}
	if meta == nil || r.bashPPNativeType(meta.typ) {
		return nil, nil, false, nil
	}
	retyped := *meta
	retyped.typ = target
	return value, &retyped, true, nil
}

// goSourceConvertedCompositeCell is goSourceConvertedComposite's value as
// the cell a declaration or argument binds, typed by the conversion target.
func (r *Runner) goSourceConvertedCompositeCell(x *syntax.BashPPConvertExpr) (*bashPPCell, bool, error) {
	value, meta, handled, err := r.goSourceConvertedComposite(x)
	if !handled || err != nil {
		return nil, handled, err
	}
	cell := &bashPPCell{declType: meta.typ}
	if named, ok := meta.typ.(*syntax.BashPPNamedType); ok && named.Name != nil {
		cell.typeName = named.Name.Value
	}
	bashPPStoreCellValue(cell, value, meta)
	return cell, true, nil
}

// goSourceStructuredIdent reports whether an identifier names a pointer or
// structured variable — one whose value is not its scalar spelling.
func (r *Runner) goSourceStructuredIdent(id *syntax.BashPPIdent) bool {
	if !r.bashPPGoSource || r.bashPPScope == nil {
		return false
	}
	cell := r.bashPPScope.lookup(id.Name.Value).view()
	return cell != nil && (cell.pointer || cell.vr.Kind == expand.Object)
}

// goSourceNilValueReceiver raises the fault Go raises when a value-receiver
// method is selected through a nil pointer: the selection dereferences the
// pointer to copy the receiver, and a nil one is the nil dereference. It
// reports whether the fault was raised, which classic Bash++ declines so
// its refusal stands.
func (r *Runner) goSourceNilValueReceiver() bool {
	if !r.bashPPGoSource {
		return false
	}
	r.goSourceRuntimeFault(errBashPPNilDereference)
	return true
}

// goSourceNilInterfaceOperand reports whether a selector's operand is a
// variable holding a nil interface, whose method selection is Go's nil
// dereference.
func (r *Runner) goSourceNilInterfaceOperand(expr syntax.BashPPExpr) bool {
	if !r.bashPPGoSource || r.bashPPScope == nil {
		return false
	}
	for {
		paren, ok := expr.(*syntax.BashPPParenExpr)
		if !ok {
			break
		}
		expr = paren.X
	}
	id, ok := expr.(*syntax.BashPPIdent)
	if !ok {
		return false
	}
	cell := r.bashPPScope.lookup(id.Name.Value).view()
	return cell != nil && cell.interfaceValue != nil && cell.interfaceValue.nilIface && cell.vr.Kind != expand.Object
}

// bashPPReportFault reports an error a method binding met: a run-time fault
// in Go source is raised as its panic, anything else is the diagnostic it
// was, with the status a binding failure reports.
func (r *Runner) bashPPReportFault(err error) {
	if err = r.goSourceRuntimeFault(err); errors.Is(err, errBashPPScalarInterrupted) {
		return
	}
	r.errf("%v\n", err)
	r.exit.code = 2
}

// goSourceMethodValueCandidate is the closure a method value selector
// denotes when it is assigned — `f = v.M` — the same binding `f := v.M`
// makes. Selecting through a nil pointer or nil interface raises the fault
// the binding meets, as an error the caller leaves unreported.
func (r *Runner) goSourceMethodValueCandidate(expr syntax.BashPPExpr) (*bashPPCell, bool, error) {
	if !r.bashPPGoSource {
		return nil, false, nil
	}
	method, ok := expr.(*syntax.BashPPSelectorExpr)
	if !ok || !method.MethodValue || r.bashPPNativeExpr(method.X) {
		return nil, false, nil
	}
	cell, err := r.goSourceLocalMethodValue(method)
	if err != nil {
		return nil, true, r.goSourceRuntimeFault(err)
	}
	return cell, true, nil
}

// goSourceNestedDeferRunning decides whether a panic keeps running — not
// halting — when a frame returns. Only a frame called by a deferred call of
// the running panic entered with running set; it stays set on return unless
// a panic raised inside the frame is still unwinding (the chain is longer
// than at entry), which abandons the rest of the deferred call as Go
// abandons it. A frame entered with no panic running never sets it: its own
// defers ran for a panic that is halting again when they finish.
func (r *Runner) goSourceNestedDeferRunning(enteredRunning bool, enteredChain int) bool {
	return enteredRunning && r.bashPPPanic.active && len(r.bashPPPanic.chain) <= enteredChain
}

// goSourceRecoverBridgeValue is the value `recover()` hands to a dependency
// — `fmt.Println(recover())`: the recovered value, or the nil interface
// when nothing was panicking, which crosses as nil rather than being
// refused as a non-scalar.
func (r *Runner) goSourceRecoverBridgeValue(expr syntax.BashPPExpr) (bashPPBridgeValue, bool, error) {
	if !r.bashPPGoSource || !bashPPRecoverExpr(expr) || r.bashPPFuncs["recover"] != nil || (r.bashPPScope != nil && r.bashPPScope.lookup("recover") != nil) {
		return bashPPBridgeValue{}, false, nil
	}
	iv, _ := r.bashPPRecoverInterfaceValue()
	if iv == nil || iv.nilIface || iv.cell == nil {
		return bashPPBridgeValue{Kind: "nil"}, true, nil
	}
	value, err := r.bashPPBridgeCell(iv.cell)
	return value, true, err
}

// goSourceRangeDerefArray ranges `for i := range (*p)` — an explicit
// dereference of a pointer to an array — through goSourceRangePointerArray
// on the pointer itself, before the operand is read: with at most one
// iteration variable Go does not evaluate `*p` at all, so a nil p ranges
// its indices instead of faulting.
func (r *Runner) goSourceRangeDerefArray(ctx context.Context, rng *syntax.BashPPRange, cell *bashPPCell) bool {
	if !r.bashPPGoSource || cell == nil || !cell.pointer {
		return false
	}
	expr := rng.Expr
	for {
		paren, ok := expr.(*syntax.BashPPParenExpr)
		if !ok {
			break
		}
		expr = paren.X
	}
	deref, ok := expr.(*syntax.BashPPDerefExpr)
	if !ok {
		return false
	}
	operand := deref.X
	for {
		paren, ok := operand.(*syntax.BashPPParenExpr)
		if !ok {
			break
		}
		operand = paren.X
	}
	if _, ok := operand.(*syntax.BashPPIdent); !ok {
		return false
	}
	return r.goSourceRangePointerArray(ctx, rng, cell.pointerValue, bashPPPointerMeta(cell.declType))
}
