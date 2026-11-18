// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"context"
	"go/constant"
	"math"
	"reflect"
	"strconv"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Sprint: #376; Story: #1551; Story-ID: b22ffea69b27
//
// Typed int evaluation without the constant carrier.
//
// The general scalar evaluator represents every operand as a go/constant value
// rebuilt from its cell's text, and routes each operator through the untyped
// constant rules before wrapping the result back into its Go type. That is the
// right authority for the whole language, but a loop over a large array pays it
// for every element: reading `x[i] != k*N+i` costs a dozen allocations and
// several scope walks per iteration.
//
// This file evaluates the subset whose result that machinery cannot change: a
// side-effect-free expression over Go `int` variables, integer constants and
// `int` elements of an interpreter-owned array or slice. Go defines such an
// expression completely — two's-complement wrapping arithmetic on a
// 64-bit int — so the host's int64 operators ARE its semantics.
//
// The contract with the general evaluator is one-directional. Every function
// here either returns the value the general evaluator would return, or reports
// ok=false having changed nothing, and the caller then takes the general path
// from the start. Nothing in the subset can run guest code, receive, allocate
// guest-visible storage or report a diagnostic, so declining midway through an
// expression is unobservable. Anything that needs a diagnostic or a panic (a
// bounds fault, a division by zero, an unrepresentable constant) is declined
// so the general evaluator raises it with its own text and position.

// bashPPFastInt is one operand of the typed int subset.
type bashPPFastInt struct {
	n int64
	// typed is false for an untyped constant operand, which takes the type of
	// the operand it is combined with.
	typed bool
	// runtime is false for a constant, whose arithmetic Go evaluates exactly
	// rather than wrapping.
	runtime bool
}

// bashPPFastIntScalar evaluates expr when it is a typed runtime int expression
// or a comparison of two such operands. Constant expressions are declined:
// their exact carrier (an untyped float constant such as 2e6, say) is part of
// what the general evaluator returns.
func (r *Runner) bashPPFastIntScalar(expr syntax.BashPPExpr) (bashPPScalar, bool) {
	if !r.bashPPGoSource || strconv.IntSize != 64 || r.bashPPScope == nil {
		return bashPPScalar{}, false
	}
	// Decide on the node's shape before touching the type table: this runs
	// ahead of every scalar evaluation, most of which are not in the subset.
	switch expr.(type) {
	case *syntax.BashPPIdent, *syntax.BashPPBinaryExpr, *syntax.BashPPIndexExpr,
		*syntax.BashPPParenExpr, *syntax.BashPPUnaryExpr, *syntax.BashPPConvertExpr,
		*syntax.BashPPSelectorExpr:
	default:
		return bashPPScalar{}, false
	}
	if _, shadowed := r.bashPPTypes["int"]; shadowed {
		return bashPPScalar{}, false
	}
	for {
		paren, ok := expr.(*syntax.BashPPParenExpr)
		if !ok {
			break
		}
		expr = paren.X
	}
	if binary, ok := expr.(*syntax.BashPPBinaryExpr); ok && binary.Op != nil {
		if truth, ok := r.bashPPFastIntCompare(binary); ok {
			return bashPPScalar{value: constant.MakeBool(truth)}, true
		}
	}
	value, ok := r.bashPPFastIntExpr(expr)
	if !ok || !value.typed || !value.runtime {
		return bashPPScalar{}, false
	}
	return bashPPScalar{value: constant.MakeInt64(value.n), typ: "int", runtime: true}, true
}

// bashPPFastIntCompare evaluates a comparison of two int operands, at least one
// of which is a typed runtime value.
func (r *Runner) bashPPFastIntCompare(x *syntax.BashPPBinaryExpr) (truth, ok bool) {
	switch x.Op.Value {
	case "==", "!=", "<", "<=", ">", ">=":
	default:
		return false, false
	}
	left, ok := r.bashPPFastIntExpr(x.X)
	if !ok {
		return false, false
	}
	right, ok := r.bashPPFastIntExpr(x.Y)
	if !ok || !bashPPFastIntOperands(left, right) {
		return false, false
	}
	switch x.Op.Value {
	case "==":
		return left.n == right.n, true
	case "!=":
		return left.n != right.n, true
	case "<":
		return left.n < right.n, true
	case "<=":
		return left.n <= right.n, true
	case ">":
		return left.n > right.n, true
	}
	return left.n >= right.n, true
}

// bashPPFastIntOperands reports whether a binary operation over the two
// operands is a runtime int operation. Two constants are left to the general
// evaluator, which folds them exactly and diagnoses overflow.
func bashPPFastIntOperands(left, right bashPPFastInt) bool {
	return left.typed && left.runtime || right.typed && right.runtime
}

func (r *Runner) bashPPFastIntExpr(expr syntax.BashPPExpr) (bashPPFastInt, bool) {
	switch x := expr.(type) {
	case *syntax.BashPPParenExpr:
		return r.bashPPFastIntExpr(x.X)
	case *syntax.BashPPBasicLit:
		if x.Kind != "INT" || x.Value == nil {
			return bashPPFastInt{}, false
		}
		n, ok := bashPPFastIntText(x.Value.Value)
		return bashPPFastInt{n: n}, ok
	case *syntax.BashPPIdent:
		if x.Name == nil {
			return bashPPFastInt{}, false
		}
		return r.bashPPFastIntCell(r.bashPPScope.lookup(x.Name.Value))
	case *syntax.BashPPUnaryExpr:
		if x.Op == nil || x.Op.Value != "-" && x.Op.Value != "+" {
			return bashPPFastInt{}, false
		}
		value, ok := r.bashPPFastIntExpr(x.X)
		if !ok || !value.typed || !value.runtime {
			return bashPPFastInt{}, false
		}
		if x.Op.Value == "-" {
			value.n = -value.n
		}
		return value, true
	case *syntax.BashPPBinaryExpr:
		if x.Op == nil {
			return bashPPFastInt{}, false
		}
		op := x.Op.Value
		switch op {
		case "+", "-", "*", "/", "%", "&", "|", "^", "&^":
		default:
			return bashPPFastInt{}, false
		}
		left, ok := r.bashPPFastIntExpr(x.X)
		if !ok {
			return bashPPFastInt{}, false
		}
		right, ok := r.bashPPFastIntExpr(x.Y)
		if !ok || !bashPPFastIntOperands(left, right) {
			return bashPPFastInt{}, false
		}
		out := bashPPFastInt{typed: true, runtime: true}
		switch op {
		case "+":
			out.n = left.n + right.n
		case "-":
			out.n = left.n - right.n
		case "*":
			out.n = left.n * right.n
		case "/", "%":
			// A zero divisor is Go's run-time panic, and the one quotient
			// that overflows is left to the evaluator that owns wrapping.
			if right.n == 0 || left.n == math.MinInt64 && right.n == -1 {
				return bashPPFastInt{}, false
			}
			if op == "/" {
				out.n = left.n / right.n
			} else {
				out.n = left.n % right.n
			}
		case "&":
			out.n = left.n & right.n
		case "|":
			out.n = left.n | right.n
		case "^":
			out.n = left.n ^ right.n
		case "&^":
			out.n = left.n &^ right.n
		}
		return out, true
	case *syntax.BashPPIndexExpr:
		return r.bashPPFastIntIndex(x)
	case *syntax.BashPPSelectorExpr:
		return r.bashPPFastIntSelector(x)
	case *syntax.BashPPConvertExpr:
		// `int(v)` of an int operand is the operand with the type int. The
		// Go front end spells an untyped constant's implicit conversion this
		// way — `k*N` with `const N = 2e6` arrives as `k*int(N)` — and a
		// constant stays a constant through it.
		if x.GoStringConstant || r.bashPPConvertTargetName(x) != "int" {
			return bashPPFastInt{}, false
		}
		if x.ConvTypeExpr != nil && !bashPPFastIntType(x.ConvTypeExpr) {
			return bashPPFastInt{}, false
		}
		value, ok := r.bashPPFastIntExpr(x.X)
		value.typed = true
		return value, ok
	}
	return bashPPFastInt{}, false
}

// bashPPFastIntCell reads a binding the general evaluator would decode as an
// int: a variable declared `int`, or an integer-valued constant that is either
// untyped or declared `int`.
func (r *Runner) bashPPFastIntCell(cell *bashPPCell) (bashPPFastInt, bool) {
	// A guarded cell is shared with an interpreted goroutine and is read
	// through a snapshot; that protocol stays with the general evaluator.
	if cell == nil || cell.guard != nil || cell.vr.Kind != expand.String || !cell.vr.Set ||
		cell.pointer || cell.interfaceValue != nil || cell.negativeZero ||
		cell.hasNonFinite || cell.hasNonFiniteComplex {
		return bashPPFastInt{}, false
	}
	typed := false
	switch {
	case cell.typeName != "" && cell.typeName != "int":
		return bashPPFastInt{}, false
	case cell.declType != nil:
		if !bashPPFastIntType(cell.declType) {
			return bashPPFastInt{}, false
		}
		typed = true
	case cell.typeName != "":
		// A named type without a declaration is not a shape the declaring
		// paths produce for an int.
		return bashPPFastInt{}, false
	}
	if cell.constant {
		if cell.exactScalar == nil {
			return bashPPFastInt{}, false
		}
		// An untyped constant such as 2e6 is an integer wherever it meets
		// an int operand; one with a fraction is not an int operand at all.
		value := cell.exactScalar
		if value.Kind() == constant.Float && !typed {
			value = constant.ToInt(value)
		}
		if value.Kind() != constant.Int {
			return bashPPFastInt{}, false
		}
		n, exact := constant.Int64Val(value)
		return bashPPFastInt{n: n, typed: typed}, exact
	}
	if !typed || cell.scalarKind != constant.Unknown && cell.scalarKind != constant.Int {
		return bashPPFastInt{}, false
	}
	n, ok := bashPPFastIntText(cell.vr.Str)
	return bashPPFastInt{n: n, typed: true, runtime: true}, ok
}

// bashPPFastIntIndex reads `x[i]` where x names an interpreter-owned array or
// slice of int. Only an in-range element already stored as an int qualifies;
// every other state, including the bounds fault, belongs to the general reader.
func (r *Runner) bashPPFastIntIndex(x *syntax.BashPPIndexExpr) (bashPPFastInt, bool) {
	if x.GoString {
		return bashPPFastInt{}, false
	}
	base := x.X
	for {
		paren, ok := base.(*syntax.BashPPParenExpr)
		if !ok {
			break
		}
		base = paren.X
	}
	ident, ok := base.(*syntax.BashPPIdent)
	if !ok || ident.Name == nil {
		return bashPPFastInt{}, false
	}
	cell := r.bashPPScope.lookup(ident.Name.Value)
	if cell == nil || cell.guard != nil || cell.pointer || cell.interfaceValue != nil ||
		cell.unsafeAllocation != nil || cell.vr.Kind != expand.Object {
		return bashPPFastInt{}, false
	}
	meta := cell.valueMeta
	if meta == nil && cell.object != nil {
		meta = cell.object.collection
	}
	if meta == nil || meta.kind != "array" && meta.kind != "inferred-array" && meta.kind != "slice" {
		return bashPPFastInt{}, false
	}
	sequence, ok := cell.vr.Obj.([]any)
	if !ok {
		return bashPPFastInt{}, false
	}
	index, ok := r.bashPPFastIntExpr(x.Index)
	if !ok || index.n < 0 || index.n >= int64(len(sequence)) || index.n >= int64(len(meta.sequence)) {
		return bashPPFastInt{}, false
	}
	// The payload decides first: an element with metadata, or one stored
	// under any carrier but the host int, is not a plain int element.
	n, ok := sequence[index.n].(int)
	if !ok || meta.sequence[index.n] != nil {
		return bashPPFastInt{}, false
	}
	collection, ok := r.bashPPUnderlyingType(meta.typ).(*syntax.BashPPCollectionType)
	if !ok || !bashPPFastIntType(collection.Element) {
		return bashPPFastInt{}, false
	}
	return bashPPFastInt{n: int64(n), typed: true, runtime: true}, true
}

// bashPPFastIntSelector reads `v.f` or `p.f` where v names a struct variable,
// p a pointer to one, and f a declared `int` field stored as a plain host int
// with no metadata of its own. The storage is reached through the same reader
// the general evaluator uses; only guarded (goroutine-shared) cells, unsafe
// views, promoted fields and every other state are declined.
func (r *Runner) bashPPFastIntSelector(x *syntax.BashPPSelectorExpr) (bashPPFastInt, bool) {
	if x.Sel == nil {
		return bashPPFastInt{}, false
	}
	ident, ok := x.X.(*syntax.BashPPIdent)
	if !ok || ident.Name == nil {
		return bashPPFastInt{}, false
	}
	cell := r.bashPPScope.lookup(ident.Name.Value)
	if cell == nil || cell.guard != nil || cell.interfaceValue != nil || cell.unsafeAllocation != nil {
		return bashPPFastInt{}, false
	}
	var value any
	var meta *bashPPCollectionMeta
	if cell.pointer {
		ptr := cell.pointerValue
		if ptr == nil || ptr.cold != nil || len(ptr.path) != 0 || ptr.target == nil ||
			ptr.target.guard != nil || ptr.target.pointer || ptr.target.interfaceValue != nil ||
			ptr.target.unsafeAllocation != nil || ptr.target.vr.Kind != expand.Object {
			return bashPPFastInt{}, false
		}
		value, meta = ptr.target.vr.Obj, bashPPCellMeta(ptr.target)
	} else {
		if cell.vr.Kind != expand.Object {
			return bashPPFastInt{}, false
		}
		value, meta = cell.vr.Obj, bashPPCellMeta(cell)
	}
	mapping, ok := value.(map[string]any)
	if !ok || meta == nil || meta.kind != "struct" {
		return bashPPFastInt{}, false
	}
	sel := r.bashPPResolveField(meta.typ, x.Sel.Value)
	if sel.ambiguous || len(sel.edges) != 1 || sel.edges[0].pointer || !bashPPFastIntType(sel.fieldType) {
		return bashPPFastInt{}, false
	}
	field, found := bashPPStorageGet(mapping, sel.edges[0].name)
	if !found || bashPPLayoutGet(meta.mapping, sel.edges[0].name) != nil {
		return bashPPFastInt{}, false
	}
	n, ok := field.(int)
	if !ok {
		return bashPPFastInt{}, false
	}
	return bashPPFastInt{n: int64(n), typed: true, runtime: true}, true
}

// bashPPFastIntType reports whether typ is spelled as the predeclared int.
// A defined or aliased type is declined rather than resolved: its operands
// carry their own type name through the general evaluator.
func bashPPFastIntType(typ syntax.BashPPTypeExpr) bool {
	named, ok := typ.(*syntax.BashPPNamedType)
	return ok && named.Name != nil && named.Name.Value == "int"
}

// bashPPFastIntText decodes the canonical decimal spelling the interpreter
// stores an int under. Any other spelling — a sign-only or empty text, a
// leading zero, a base prefix, a digit separator — is declined, so the general
// evaluator's literal rules stay the only reading of it.
func bashPPFastIntText(text string) (int64, bool) {
	digits := text
	negative := false
	if len(digits) > 0 && digits[0] == '-' {
		negative, digits = true, digits[1:]
	}
	// Eighteen digits cannot overflow an int64; longer spellings are rare
	// enough to leave to the general evaluator.
	if len(digits) == 0 || len(digits) > 18 || digits[0] == '0' && len(digits) > 1 {
		return 0, false
	}
	var n int64
	for i := 0; i < len(digits); i++ {
		c := digits[i]
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int64(c-'0')
	}
	if negative {
		if n == 0 {
			return 0, false
		}
		n = -n
	}
	return n, true
}

// bashPPFastIntIndexAssign performs `x[i] = v` where x names an
// interpreter-owned array or slice of int and v is an int operand, replacing
// one plain int element with another. It reports false, having written
// nothing, for every target the general assignment has a rule about: a
// readonly or shared binding, a pointer, an unsafe view, an element that
// carries metadata, or an index that faults.
func (r *Runner) bashPPFastIntIndexAssign(target *syntax.BashPPIndexExpr, rhs syntax.BashPPExpr) bool {
	if !r.bashPPGoSource || strconv.IntSize != 64 || r.bashPPScope == nil || target.GoString {
		return false
	}
	ident, ok := target.X.(*syntax.BashPPIdent)
	if !ok || ident.Name == nil {
		return false
	}
	if _, shadowed := r.bashPPTypes["int"]; shadowed {
		return false
	}
	cell := r.bashPPScope.lookup(ident.Name.Value)
	if cell == nil || cell.guard != nil || cell.pointer || cell.interfaceValue != nil ||
		cell.unsafeAllocation != nil || cell.constant || cell.vr.ReadOnly ||
		cell.object == nil || cell.object.readonly || cell.vr.Kind != expand.Object {
		return false
	}
	meta := cell.valueMeta
	if meta == nil {
		meta = cell.object.collection
	}
	if meta == nil || meta.kind != "array" && meta.kind != "inferred-array" && meta.kind != "slice" {
		return false
	}
	sequence, ok := cell.vr.Obj.([]any)
	if !ok {
		return false
	}
	collection, ok := r.bashPPUnderlyingType(meta.typ).(*syntax.BashPPCollectionType)
	if !ok || !bashPPFastIntType(collection.Element) {
		return false
	}
	// Go evaluates the index, then the right side, then stores; neither
	// operand has an effect here, so only the store is observable.
	index, ok := r.bashPPFastIntExpr(target.Index)
	if !ok || index.n < 0 || index.n >= int64(len(sequence)) || index.n >= int64(len(meta.sequence)) {
		return false
	}
	if _, plain := sequence[index.n].(int); !plain || meta.sequence[index.n] != nil {
		return false
	}
	value, ok := r.bashPPFastIntExpr(rhs)
	if !ok {
		return false
	}
	sequence[index.n] = int(value.n)
	return true
}

// bashPPRangeKeyReusable reports whether one cell can carry the int key of
// every iteration of rng.
//
// Go gives each iteration its own variable, and the general loop honours that
// with a fresh scope and cell per iteration. The difference is observable only
// by something that outlives the iteration holding the VARIABLE rather than
// its value: a closure, an address, a goroutine or a deferred call. A body
// built solely from the statement and expression forms listed below can create
// none of those, and an int has no methods through which a call could take its
// address, so for such a body one reused cell is indistinguishable from fresh
// ones. Any node outside the list — known or not — keeps the general loop.
func (r *Runner) bashPPRangeKeyReusable(rng *syntax.BashPPRange) bool {
	if !r.bashPPGoSource || rng.Body == nil {
		return false
	}
	if reusable, known := r.bashPPRangeKeyReuse[rng]; known {
		return reusable
	}
	reusable := bashPPCaptureFree(reflect.ValueOf(rng.Body))
	if r.bashPPRangeKeyReuse == nil {
		r.bashPPRangeKeyReuse = make(map[*syntax.BashPPRange]bool)
	}
	r.bashPPRangeKeyReuse[rng] = reusable
	return reusable
}

// bashPPCaptureFreeTypes are the syntax nodes a capture-free loop body may be
// built from. None of them can retain a variable: they read values, assign
// values, call by value and branch.
var bashPPCaptureFreeTypes = map[reflect.Type]bool{
	reflect.TypeFor[syntax.Block]():             true,
	reflect.TypeFor[syntax.Stmt]():              true,
	reflect.TypeFor[syntax.Lit]():               true,
	reflect.TypeFor[syntax.Pos]():               true,
	reflect.TypeFor[syntax.Comment]():           true,
	reflect.TypeFor[syntax.Word]():              true,
	reflect.TypeFor[syntax.SglQuoted]():         true,
	reflect.TypeFor[syntax.DblQuoted]():         true,
	reflect.TypeFor[syntax.StartSite]():         true,
	reflect.TypeFor[syntax.BashPPIf]():          true,
	reflect.TypeFor[syntax.BashPPAssign]():      true,
	reflect.TypeFor[syntax.BashPPCall]():        true,
	reflect.TypeFor[syntax.BashPPBranch]():      true,
	reflect.TypeFor[syntax.BashPPIdent]():       true,
	reflect.TypeFor[syntax.BashPPBasicLit]():    true,
	reflect.TypeFor[syntax.BashPPBinaryExpr]():  true,
	reflect.TypeFor[syntax.BashPPUnaryExpr]():   true,
	reflect.TypeFor[syntax.BashPPParenExpr]():   true,
	reflect.TypeFor[syntax.BashPPIndexExpr]():   true,
	reflect.TypeFor[syntax.BashPPConvertExpr](): true,
	// A call records the names of its result types.
	reflect.TypeFor[syntax.BashPPNamedType](): true,
}

func bashPPCaptureFree(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return true
		}
		return bashPPCaptureFree(v.Elem())
	case reflect.Struct:
		if !bashPPCaptureFreeTypes[v.Type()] {
			return false
		}
		for i := range v.NumField() {
			if !bashPPCaptureFree(v.Field(i)) {
				return false
			}
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			if !bashPPCaptureFree(v.Index(i)) {
				return false
			}
		}
	case reflect.Map, reflect.Func, reflect.Chan, reflect.UnsafePointer:
		return v.IsNil()
	}
	return true
}

// bashPPRangeReusedKey runs iterations first..length-1 of a key-only range
// over an array or slice with one iteration scope and one key cell, and
// returns the index at which the general loop must take over, or length when
// the loop is finished. finished is true when the loop was left early.
//
// The reused cell is re-established before every iteration, so an assignment
// to the key inside the body lasts for that iteration only, as it does for a
// fresh variable. Should the body nonetheless have shared or replaced the
// binding, the remaining iterations are handed back.
func (r *Runner) bashPPRangeReusedKey(ctx context.Context, rng *syntax.BashPPRange, keyType syntax.BashPPTypeExpr, length int) (next int, finished bool) {
	if length == 0 || !bashPPRangeBindsNames(rng) || bashPPRangeBindsValue(rng) || rng.Names[0].Value == "_" || !r.bashPPRangeKeyReusable(rng) {
		return 0, false
	}
	name := rng.Names[0].Value
	leave := r.bashPPPushScope()
	defer leave()
	scope := r.bashPPScope
	r.bashPPDeclareRangeValue(name, 0, keyType, nil)
	cell := scope.entries[name]
	if cell == nil || cell.guard != nil || len(scope.entries) != 1 {
		// The declaration did not produce the plain binding this loop
		// reuses; nothing has run yet, so the general loop starts over.
		return 0, false
	}
	template := *cell
	body := r.bashPPTaskContext(ctx)
	for i := 0; i < length; i++ {
		if i > 0 {
			if r.bashPPScope != scope || scope.entries[name] != cell || cell.guard != nil || len(scope.entries) != 1 {
				return i, false
			}
			*cell = template
			cell.vr.Str = strconv.Itoa(i)
		}
		r.cmd(body, rng.Body)
		if !r.bashPPRangeControl() {
			return length, true
		}
	}
	return length, false
}
