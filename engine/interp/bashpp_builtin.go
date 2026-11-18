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

// bashPPValueBuiltin reports the predeclared functions whose values or
// mutations are implemented over bashPPCell. panic/recover deliberately stay
// in bashpp_panic.go; close deliberately stays in bashpp_concurrency.go.
func bashPPValueBuiltin(name string) bool {
	switch name {
	case "append", "cap", "clear", "copy", "delete", "len", "make",
		"max", "min", "new", "print", "println":
		return true
	}
	return false
}

func (r *Runner) bashPPBuiltinError(kind, format string, args ...any) {
	r.errf("BASHPP-EBUILTIN-%s: "+format+"\n", append([]any{kind}, args...)...)
	r.exit = exitStatus{code: 2}
}

func (r *Runner) bashPPBuiltinArity(name, want string, got int) {
	r.bashPPBuiltinError("ARITY", "%s expects %s; got %d argument(s)", name, want, got)
}

type bashPPBuiltinArg struct {
	value     any
	meta      *bashPPCollectionMeta
	typ       syntax.BashPPTypeExpr
	cell      *bashPPCell
	channel   *bashPPChannel
	scalar    bashPPScalar
	hasScalar bool
	text      string
}

func (r *Runner) bashPPBuiltinArg(w *syntax.Word) bashPPBuiltinArg {
	text := bashPPWordSource(w)
	if text == "nil" {
		return bashPPBuiltinArg{text: text}
	}
	// One snapshot describes the argument: the kind test and the payload it
	// selects, and every later reader of arg.cell, answer about one store of
	// a cell an interpreted goroutine may be rebinding.
	if cell := r.bashPPCellForWord(w).view(); cell != nil {
		arg := bashPPBuiltinArg{cell: cell, typ: cell.declType, channel: cell.channel, text: text}
		if arg.typ == nil && cell.typeName != "" {
			arg.typ = &syntax.BashPPNamedType{Name: &syntax.Lit{Value: cell.typeName}}
		}
		if cell.interfaceValue != nil {
			arg.meta = &bashPPCollectionMeta{kind: "interface", typ: arg.typ, interfaceValue: cell.interfaceValue}
			if source := cell.interfaceValue.cell; source != nil {
				arg.value = source.vrValue()
			}
		} else if cell.pointer {
			arg.value, arg.meta = cell.pointerValue, bashPPPointerMeta(cell.declType)
		} else if cell.vr.Kind == expand.Object {
			arg.value, arg.meta = cell.vr.Obj, bashPPCellMeta(cell)
			if arg.typ == nil && arg.meta != nil {
				arg.typ = arg.meta.typ
			}
		} else {
			arg.scalar = r.bashPPScalarFromCell(cell)
			arg.value = bashPPBuiltinExactScalarValue(cell.vr.Str, arg.scalar)
			arg.hasScalar = true
		}
		return arg
	}
	if value, ok := r.bashPPResolveWord(w); ok {
		scalar := bashPPScalarFromString(value)
		return bashPPBuiltinArg{value: bashPPBuiltinExactScalarValue(value, scalar), scalar: scalar, hasScalar: true, text: text}
	}
	value := r.bashPPExprValue(w)
	if len(w.Parts) == 1 {
		switch w.Parts[0].(type) {
		case *syntax.SglQuoted, *syntax.DblQuoted:
			return bashPPBuiltinArg{value: value, scalar: bashPPScalar{value: constant.MakeString(value)}, hasScalar: true, text: text}
		}
	}
	scalar := bashPPScalarFromString(value)
	return bashPPBuiltinArg{value: bashPPBuiltinExactScalarValue(value, scalar), scalar: scalar, hasScalar: true, text: text}
}

func bashPPBuiltinExactScalarValue(text string, scalar bashPPScalar) any {
	if scalar.hasNonFiniteComplex {
		return scalar.nonFiniteComplex
	}
	if scalar.hasNonFinite {
		return scalar.nonFinite
	}
	if scalar.value == nil {
		return text
	}
	switch scalar.value.Kind() {
	case constant.String:
		return constant.StringVal(scalar.value)
	case constant.Bool:
		return constant.BoolVal(scalar.value)
	case constant.Int:
		if value, ok := constant.Int64Val(scalar.value); ok {
			return int(value)
		} else {
			return text
		}
	case constant.Float:
		value, _ := constant.Float64Val(scalar.value)
		return value
	case constant.Complex:
		value := bashPPComplexNumber(scalar.value)
		if scalar.typ == "complex64" {
			return complex64(value)
		}
		return value
	}
	return text
}

func bashPPBuiltinScalar(value any) string {
	switch value := value.(type) {
	case string:
		return value
	case nil:
		return "<nil>"
	default:
		return fmt.Sprint(value)
	}
}

func bashPPBuiltinScalarCell(value string) *bashPPCell {
	return &bashPPCell{vr: expand.Variable{Set: true, Kind: expand.String, Str: value}, scalarKind: bashPPScalarFromString(value).value.Kind()}
}

func (r *Runner) bashPPBuiltinMutable(name string, arg bashPPBuiltinArg) bool {
	if arg.cell == nil {
		r.bashPPBuiltinError("TYPE", "%s first argument must be an addressable collection", name)
		return false
	}
	if arg.cell.constant || arg.cell.vr.ReadOnly || arg.cell.object != nil && arg.cell.object.readonly {
		owner := arg.text
		if arg.cell.object != nil && arg.cell.object.owner != "" {
			owner = arg.cell.object.owner
		}
		r.errf("BASHPP-EREADONLY-MUTATION: cannot mutate readonly value %q through %s\n", owner, name)
		r.exit = exitStatus{code: 2}
		return false
	}
	return true
}

func (r *Runner) bashPPBuiltinCollection(arg bashPPBuiltinArg, kinds ...string) (*syntax.BashPPCollectionType, bool) {
	if arg.meta == nil {
		return nil, false
	}
	// A collection parameter in a generic body can retain the declaration's
	// spelling in its metadata even though the running frame has instantiated
	// it. Resolve that spelling at the builtin boundary: append needs the
	// concrete element for assignability, and its result must not carry S/T
	// into later indexed reads. This is deliberately frame-local; rewriting the
	// shared metadata would leak the first instantiation into later calls.
	typ := r.bashPPBindTypeExpr(arg.meta.typ)
	shape, ok := r.bashPPUnderlyingType(typ).(*syntax.BashPPCollectionType)
	if !ok {
		return nil, false
	}
	shape, _ = r.bashPPBindTypeExpr(shape).(*syntax.BashPPCollectionType)
	for _, kind := range kinds {
		if shape.Kind == kind && bashPPBuiltinCollectionStorage(arg.value, kind) {
			return shape, true
		}
	}
	return nil, false
}

func bashPPBuiltinCollectionStorage(value any, kind string) bool {
	switch kind {
	case "slice", "array", "inferred-array":
		_, ok := value.([]any)
		return ok || value == nil
	case "map":
		_, ok := value.(map[string]any)
		return ok || value == nil
	default:
		return false
	}
}

func (r *Runner) bashPPBuiltinPointerArrayLen(arg bashPPBuiltinArg) (int, bool) {
	ptr, ok := r.bashPPPointerType(arg.typ)
	if !ok {
		return 0, false
	}
	array, ok := r.bashPPUnderlyingType(ptr.Element).(*syntax.BashPPCollectionType)
	if !ok || array.Kind != "array" || array.Length == nil {
		return 0, false
	}
	n, err := r.bashPPArrayLength(array.Length.Value)
	return n, err == nil
}

func (r *Runner) goSourceStaticArrayLength(name string, call *syntax.BashPPCall) (*bashPPCell, bool) {
	if !r.bashPPGoSource || (name != "len" && name != "cap") || call.GoRuntime || len(call.ArgExprs) != 1 || call.Ellipsis.IsValid() {
		return nil, false
	}
	typ, ok := r.goSourceStaticExprType(call.ArgExprs[0])
	if !ok {
		return nil, false
	}
	if ptr, pointer := r.bashPPPointerType(typ); pointer {
		typ = ptr.Element
	}
	array, ok := r.bashPPUnderlyingType(typ).(*syntax.BashPPCollectionType)
	if !ok || array.Kind != "array" || array.Length == nil {
		return nil, false
	}
	size, err := r.bashPPArrayLength(array.Length.Value)
	if err != nil {
		return nil, false
	}
	return bashPPBuiltinScalarCell(strconv.Itoa(size)), true
}

func (r *Runner) bashPPBuiltinByteSlice(shape *syntax.BashPPCollectionType) bool {
	typ := r.bashPPCanonicalAssignableType(shape.Element)
	named, ok := typ.(*syntax.BashPPNamedType)
	return ok && (named.Name.Value == "byte" || named.Name.Value == "uint8")
}

func (r *Runner) bashPPBuiltinInt(name string, arg bashPPBuiltinArg) (int, bool) {
	var text string
	switch value := arg.value.(type) {
	case int:
		return value, true
	case string:
		text = value
	default:
		text = fmt.Sprint(value)
	}
	n, err := strconv.Atoi(text)
	if err != nil {
		r.bashPPBuiltinError("TYPE", "%s size must be an integer; got %s", name, arg.text)
		return 0, false
	}
	return n, true
}

func (r *Runner) bashPPBuiltinElement(arg bashPPBuiltinArg, expected syntax.BashPPTypeExpr) (any, *bashPPCollectionMeta, bool) {
	// append/copy are evaluated after their collection operand has established
	// the element type, but a generic body's syntax can still spell that type as
	// its parameter. Bind the destination here and keep arg.cell/arg.meta as the
	// source of truth; converting the argument text would lose map-range keys
	// and structured or reference identity.
	expected = r.bashPPBindTypeExpr(expected)
	// append into an interface slice performs an interface assignment, not a
	// scalar conversion. Keep the source cell so the interface element retains
	// its dynamic type and callable/channel/structured side channels for later
	// assertions and dispatch.
	if _, ok := r.bashPPInterfaceType(expected); ok && arg.cell != nil {
		// Assigning an interface value to another interface stores its dynamic
		// value, not a second interface wrapper. An indexed interface element
		// arrives with that value in interfaceValue; bashPPCopyInterfaceCell
		// intentionally removes the wrapper, so copy it explicitly before the
		// ordinary concrete-to-interface path below.
		if source := arg.cell.interfaceValue; source != nil {
			stored := *source
			if source.cell != nil {
				stored.cell = bashPPCopyInterfaceCell(source.cell)
			}
			meta := &bashPPCollectionMeta{kind: "interface", typ: expected, interfaceValue: &stored}
			if stored.nilIface || stored.cell == nil {
				return "", meta, true
			}
			return stored.cell.vrValue(), meta, true
		}
		stored := bashPPCopyInterfaceCell(arg.cell)
		if err := r.bashPPBindInterfaceParam(stored, expected); err != nil {
			r.bashPPBuiltinError("TYPE", "%v", err)
			return nil, nil, false
		}
		if stored.interfaceValue != nil {
			meta := &bashPPCollectionMeta{kind: "interface", typ: expected, interfaceValue: stored.interfaceValue}
			return stored.vrValue(), meta, true
		}
	}
	// A channel argument carries its identity on the cell, not in a value the
	// scalar check below could see. Store it the way a composite literal
	// element does, so `a = append(a, make(chan bool))` keeps the reference.
	if _, ok := r.goSourceChannelType(expected); ok && arg.meta == nil {
		cell := arg.cell
		if cell == nil && arg.channel != nil {
			cell = &bashPPCell{channel: arg.channel}
		}
		if cell != nil {
			meta := &bashPPCollectionMeta{kind: "channel", typ: expected}
			value, err := r.goSourceChannelCellValue(cell, expected, meta)
			if err != nil {
				r.bashPPBuiltinError("TYPE", "%v", err)
				return nil, nil, false
			}
			return value, meta, true
		}
	}
	if arg.meta != nil {
		if err := r.bashPPCheckTypedValue(arg.value, arg.meta, expected); err != nil {
			r.bashPPBuiltinError("TYPE", "%v", err)
			return nil, nil, false
		}
		value, meta := bashPPCopyArrayValue(arg.value, arg.meta)
		return value, meta, true
	}
	// A function value — a closure returned by a call, a func-typed parameter
	// that arrived through an interface method, a named function — is its
	// handle in the scalar slot. `append(c.calls, fn)` binds it to the
	// element signature the way a composite literal element does.
	if stored, claimed, err := r.bashPPCollectionFuncElement(arg.value, expected); claimed {
		if err != nil {
			r.bashPPBuiltinError("TYPE", "%v", err)
			return nil, nil, false
		}
		return stored, nil, true
	}
	if err := r.bashPPCheckCollectionValue(arg.value, expected); err != nil {
		// A builtin argument can arrive as a dependency carrier without cell
		// metadata (`append(frames, frame)` with a native element type).
		// Claim it the way a composite literal element would, keeping the
		// handle in its authenticated session and non-finite floats wrapped.
		bridged, bridgedMeta, claimed, bridgeErr := r.bashPPCollectionBridgeValue(arg.value, expected)
		if !claimed {
			r.bashPPBuiltinError("TYPE", "%v", err)
			return nil, nil, false
		}
		if bridgeErr != nil {
			r.bashPPBuiltinError("TYPE", "%v", bridgeErr)
			return nil, nil, false
		}
		return bridged, bridgedMeta, true
	}
	return arg.value, nil, true
}

// bashPPRunValueBuiltin executes a non-panic predeclared call. Its result is a
// full cell so structured identity and named type metadata survive `:=`.
func (r *Runner) bashPPRunValueBuiltin(name string, c *syntax.BashPPCall) (*bashPPCell, bool) {
	if cell, handled := r.bashPPSprint162MakeChannelBuiltin(name, c); handled {
		return cell, cell != nil
	}
	if r.bashPPGoSource && name == "make" {
		if typ, ok := c.ArgType.(*syntax.BashPPChanType); ok {
			var expr syntax.BashPPExpr
			var word *syntax.Word
			if len(c.ArgExprs) > 1 {
				expr = c.ArgExprs[1]
			}
			if len(c.Args) > 1 {
				word = c.Args[1]
			}
			cell, handled, err := r.goSourceMakeNativeChannel(typ, expr, word)
			if handled {
				if err != nil {
					r.goSourceNativeChannelError(err)
					return nil, false
				}
				return cell, true
			}
		}
	}
	// A checked constant len/cap of an array-shaped expression reads only the
	// operand's static type. In particular, len(*p) must not dereference a nil
	// *[N]T. Calls and receives make the builtin non-constant; the Go front end
	// records that distinction in GoRuntime, so those operands continue through
	// the ordinary argument path below and are evaluated exactly once.
	if cell, ok := r.goSourceStaticArrayLength(name, c); ok {
		return cell, true
	}

	args, expanded, err := r.goSourceBuiltinTupleArgs(c)
	if err != nil {
		if !errors.Is(err, errBashPPScalarInterrupted) {
			r.exit.fatal(err)
		}
		return nil, false
	}
	if !expanded {
		args = make([]bashPPBuiltinArg, len(c.Args))
	}
	for i := range c.Args {
		if expanded {
			break
		}
		if r.bashPPGoSource && (name == "print" || name == "println") && i < len(c.ArgExprs) && c.ArgExprs[i] != nil {
			if r.goSourcePrintReferenceOperand(c.ArgExprs[i]) {
				text, err := r.goSourcePrintReference(c.ArgExprs[i])
				if err != nil {
					if !errors.Is(err, errBashPPScalarInterrupted) {
						r.exit.fatal(err)
					}
					return nil, false
				}
				args[i] = bashPPBuiltinArg{value: text, scalar: bashPPScalar{value: constant.MakeString(text)}, hasScalar: true, text: text}
				continue
			}
			scalar, err := r.bashPPEvalScalarExpr(c.ArgExprs[i])
			if err != nil {
				if !errors.Is(err, errBashPPScalarInterrupted) {
					r.exit.fatal(err)
				}
				return nil, false
			}
			text := bashPPScalarString(scalar.value)
			args[i] = bashPPBuiltinArg{value: bashPPBuiltinExactScalarValue(text, scalar), scalar: scalar, hasScalar: true, text: text}
			continue
		}
		if r.bashPPGoSource && name == "delete" && i == 1 && i < len(c.ArgExprs) && c.ArgExprs[i] != nil {
			if shape, ok := r.bashPPBuiltinCollection(args[0], "map"); ok {
				if _, pointerKey := r.bashPPUnderlyingType(shape.Key).(*syntax.BashPPPointerType); pointerKey {
					value, meta, err := r.bashPPEvalElement(c.ArgExprs[i], shape.Key)
					if err != nil {
						r.exit.fatal(err)
						return nil, false
					}
					args[i] = bashPPBuiltinArg{value: value, meta: meta, typ: shape.Key}
					continue
				}
			}
		}
		var err error
		args[i], err = r.goSourceBuiltinArg(c, i)
		if err != nil {
			if !errors.Is(err, errBashPPScalarInterrupted) {
				r.exit.fatal(err)
			}
			return nil, false
		}
	}
	switch name {
	case "len", "cap":
		cell, err := r.bashPPBuiltinLength(name, c, args)
		if err != nil {
			r.errf("%v\n", err)
			r.exit = exitStatus{code: 2}
			return nil, false
		}
		return cell, true

	case "append":
		if len(args) < 1 {
			r.bashPPBuiltinArity(name, "at least 1 argument", len(args))
			return nil, false
		}
		if err := r.goSourceMaterializeBuiltinBridgeCollection(&args[0]); err != nil {
			r.bashPPBuiltinError("TYPE", "%v", err)
			return nil, false
		}
		if err := r.goSourceMaterializeBuiltinSelectorCollection(&args[0]); err != nil {
			r.bashPPBuiltinError("TYPE", "%v", err)
			return nil, false
		}
		shape, ok := r.bashPPBuiltinCollection(args[0], "slice")
		if !ok {
			if args[0].value == nil {
				r.bashPPBuiltinError("NIL", "append first argument is untyped nil")
				return nil, false
			}
			r.bashPPBuiltinError("TYPE", "append first argument must be a slice")
			return nil, false
		}
		seq, _ := args[0].value.([]any)
		oldLen := bashPPSequenceLen(args[0].meta, seq)
		oldCap := bashPPSequenceCap(args[0].meta, seq)
		additional := len(args) - 1
		stringSpread := false
		if c.Ellipsis.IsValid() && len(args) == 2 {
			if err := r.goSourceMaterializeBuiltinBridgeCollection(&args[1]); err != nil {
				r.bashPPBuiltinError("TYPE", "%v", err)
				return nil, false
			}
			if err := r.goSourceMaterializeBuiltinSelectorCollection(&args[1]); err != nil {
				r.bashPPBuiltinError("TYPE", "%v", err)
				return nil, false
			}
			if text, ok := args[1].value.(string); ok && args[1].meta == nil && args[1].channel == nil && r.bashPPBuiltinByteSlice(shape) {
				additional, stringSpread = len([]byte(text)), true
			} else if spread, ok := args[1].value.([]any); ok {
				additional = bashPPSequenceLen(args[1].meta, spread)
			}
		}
		if additional > 0 && additional <= oldCap-oldLen && !r.bashPPBuiltinMutable(name, args[0]) {
			return nil, false
		}
		// The appended elements are gathered first so that all three spellings
		// grow the payload through one call, which is what reproduces Go's
		// element-size-dependent capacity; see bashPPAppendSlice in
		// bashpp_collection_growth.go.
		var added []any
		var addedMetas []*bashPPCollectionMeta
		if c.Ellipsis.IsValid() {
			if len(args) != 2 {
				r.bashPPBuiltinArity(name, "a slice and one spread slice", len(args))
				return nil, false
			}
			if stringSpread {
				for _, b := range []byte(args[1].value.(string)) {
					added, addedMetas = append(added, int(b)), append(addedMetas, nil)
				}
			} else {
				otherShape, ok := r.bashPPBuiltinCollection(args[1], "slice")
				if !ok || !r.bashPPTypeAssignable(otherShape.Element, shape.Element) {
					r.bashPPBuiltinError("TYPE", "append spread argument must be a compatible slice or string for []byte")
					return nil, false
				}
				if !bashPPLogicalSequence(args[1].meta) {
					other, _ := args[1].value.([]any)
					added = append(added, other...)
					addedMetas = append(addedMetas, args[1].meta.sequence...)
				}
			}
		} else {
			for _, arg := range args[1:] {
				value, meta, ok := r.bashPPBuiltinElement(arg, shape.Element)
				if !ok {
					return nil, false
				}
				added, addedMetas = append(added, value), append(addedMetas, meta)
			}
		}
		resultType := r.bashPPBindTypeExpr(args[0].meta.typ)
		baseMeta := *args[0].meta
		baseMeta.typ = resultType
		seq, meta, ok := r.bashPPAppendTypedSlice(seq, &baseMeta, shape.Element, additional, added, addedMetas)
		if !ok {
			if r.bashPPGoSource {
				r.bashPPRaise("runtime error: growslice: len out of range")
			} else {
				r.bashPPBuiltinError("SIZE", "append result length is invalid")
			}
			return nil, false
		}
		identity := args[0].cell.object
		if bashPPSequenceLen(meta, seq) > oldCap {
			identity = &bashPPObjectIdentity{collection: meta}
		}
		return &bashPPCell{vr: bashPPCollectionVariable(seq), object: identity, valueMeta: meta, declType: resultType}, true

	case "copy":
		if len(args) != 2 || c.Ellipsis.IsValid() {
			r.bashPPBuiltinArity(name, "exactly 2 arguments", len(args))
			return nil, false
		}
		dstType, dstOK := r.bashPPBuiltinCollection(args[0], "slice")
		srcType, srcOK := r.bashPPBuiltinCollection(args[1], "slice")
		text, stringSource := args[1].value.(string)
		stringSource = stringSource && args[1].meta == nil && args[1].channel == nil && dstOK && r.bashPPBuiltinByteSlice(dstType)
		if !dstOK || !stringSource && (!srcOK || !r.bashPPTypeAssignable(srcType.Element, dstType.Element)) {
			r.bashPPBuiltinError("TYPE", "copy arguments must be compatible slices")
			return nil, false
		}
		if !r.bashPPBuiltinMutable(name, args[0]) {
			return nil, false
		}
		dst, _ := args[0].value.([]any)
		var n int
		if stringSource {
			bytes := []byte(text)
			n = min(len(dst), len(bytes))
			for i := range n {
				dst[i], args[0].meta.sequence[i] = int(bytes[i]), nil
			}
		} else {
			src, _ := args[1].value.([]any)
			n = copy(dst, src)
			copy(args[0].meta.sequence[:n], args[1].meta.sequence[:n])
		}
		return bashPPBuiltinScalarCell(strconv.Itoa(n)), true

	case "delete":
		if len(args) != 2 || c.Ellipsis.IsValid() {
			r.bashPPBuiltinArity(name, "exactly 2 arguments", len(args))
			return nil, false
		}
		shape, ok := r.bashPPBuiltinCollection(args[0], "map")
		if !ok {
			r.bashPPBuiltinError("TYPE", "delete first argument must be a map")
			return nil, false
		}
		if !r.bashPPBuiltinMutable(name, args[0]) {
			return nil, false
		}
		if err := r.bashPPCheckCollectionValue(args[1].value, shape.Key); err != nil && !r.bashPPGoSource {
			r.bashPPBuiltinError("TYPE", "delete key: %v", err)
			return nil, false
		}
		if mapping, ok := args[0].value.(map[string]any); ok {
			keyMeta := args[1].meta
			keyType := shape.Key
			if args[1].cell != nil && args[1].cell.interfaceValue != nil {
				keyMeta = &bashPPCollectionMeta{kind: "interface", typ: args[1].cell.declType, interfaceValue: args[1].cell.interfaceValue}
			} else if _, iface := r.bashPPInterfaceType(shape.Key); iface && args[1].typ != nil {
				// An interface-keyed delete hashes the key's dynamic type at
				// run time, so an unhashable one is Go's recoverable panic,
				// not a static type error.
				if r.bashPPGoSource && !r.bashPPMapKeyType(args[1].typ) {
					r.bashPPRaise("runtime error: hash of unhashable type " + r.goSourceRuntimeTypeName(args[1].typ))
					return nil, false
				}
				keyType = args[1].typ
			}
			if err := r.bashPPSprint165MapDelete(mapping, args[0].meta, args[1].value, keyMeta, keyType); err != nil {
				// An interrupted key hash has already raised its panic; a
				// second diagnostic here would outlive the recover.
				if !r.bashPPGoSource || !errors.Is(err, errBashPPScalarInterrupted) {
					r.bashPPBuiltinError("TYPE", "delete key: %v", err)
				}
			}
		}
		return nil, false

	case "clear":
		if len(args) != 1 || c.Ellipsis.IsValid() {
			r.bashPPBuiltinArity(name, "exactly 1 argument", len(args))
			return nil, false
		}
		shape, ok := r.bashPPBuiltinCollection(args[0], "slice", "map")
		if !ok {
			r.bashPPBuiltinError("TYPE", "clear argument must be a slice or map")
			return nil, false
		}
		if !r.bashPPBuiltinMutable(name, args[0]) {
			return nil, false
		}
		if args[0].meta.kind == "map" {
			if mapping, ok := args[0].value.(map[string]any); ok {
				bashPPSprint165MapClear(mapping, args[0].meta)
			}
		} else if seq, ok := args[0].value.([]any); ok {
			for i := range seq {
				seq[i], args[0].meta.sequence[i] = r.bashPPZeroValue(shape.Element)
			}
		}
		return nil, false

	case "make":
		if len(args) < 1 || len(args) > 3 || c.ArgType == nil || c.Ellipsis.IsValid() {
			r.bashPPBuiltinArity(name, "a slice/map type and valid size arguments", len(args))
			return nil, false
		}
		typ := c.ArgType
		shape, ok := r.bashPPUnderlyingType(typ).(*syntax.BashPPCollectionType)
		if !ok || shape.Kind != "slice" && shape.Kind != "map" {
			r.bashPPBuiltinError("TYPE", "make type must be a slice or map")
			return nil, false
		}
		if shape.Kind == "map" {
			if len(args) > 2 {
				r.bashPPBuiltinArity(name, "a map type and optional size", len(args))
				return nil, false
			}
			if len(args) == 2 {
				n, ok := r.bashPPBuiltinInt(name, args[1])
				if !ok || n < 0 {
					if ok {
						r.bashPPBuiltinError("SIZE", "make map size must not be negative")
					}
					return nil, false
				}
			}
			value := make(map[string]any)
			meta := &bashPPCollectionMeta{kind: "map", typ: typ, mapping: make(map[string]*bashPPCollectionMeta), mapKeys: make(map[bashPPMapKey]*bashPPMapEntry)}
			return &bashPPCell{vr: bashPPCollectionVariable(value), object: &bashPPObjectIdentity{collection: meta}, valueMeta: meta, declType: typ}, true
		}
		if len(args) < 2 {
			r.bashPPBuiltinArity(name, "a slice type and length, with optional capacity", len(args))
			return nil, false
		}
		length, ok := r.goSourceMakeSliceSize(args[1])
		if !ok {
			return nil, false
		}
		capacity := length
		if len(args) == 3 {
			capacity, ok = r.goSourceMakeSliceSize(args[2])
			if !ok {
				return nil, false
			}
		}
		if r.goSourceMakeSliceFault(shape.Element, length, capacity) {
			return nil, false
		}
		if length < 0 || capacity < length {
			r.bashPPBuiltinError("SIZE", "make slice length/capacity is invalid: %d/%d", length, capacity)
			return nil, false
		}
		var value []any
		var children []*bashPPCollectionMeta
		if r.bashPPGoSource {
			var err error
			var logical bool
			value, children, logical, err = r.goSourceAllocateTypedSlice(shape.Element, length, capacity)
			if err != nil {
				r.exit.fatal(err)
				return nil, false
			}
			if logical {
				meta := &bashPPCollectionMeta{kind: "slice", typ: typ}
				bashPPSetSequenceShape(meta, length, capacity)
				return &bashPPCell{vr: bashPPCollectionVariable(value), object: &bashPPObjectIdentity{collection: meta}, valueMeta: meta, declType: typ}, true
			}
		} else {
			value = make([]any, length, capacity)
			children = make([]*bashPPCollectionMeta, length, capacity)
		}
		for i := range value {
			value[i], children[i] = r.bashPPZeroValue(shape.Element)
		}
		// Go's backing array is zeroed to its full capacity, so re-slicing past
		// the length has to expose typed zeros; see bashPPZeroSpareCapacity in
		// bashpp_collection_growth.go.
		r.bashPPZeroSpareCapacity(value, children, shape.Element)
		meta := &bashPPCollectionMeta{kind: "slice", typ: typ, sequence: children}
		return &bashPPCell{vr: bashPPCollectionVariable(value), object: &bashPPObjectIdentity{collection: meta}, valueMeta: meta, declType: typ}, true

	case "min", "max":
		if len(args) == 0 || c.Ellipsis.IsValid() {
			r.bashPPBuiltinArity(name, "at least 1 non-spread argument", len(args))
			return nil, false
		}
		best := 0
		var resultType syntax.BashPPTypeExpr
		kind := constant.Unknown
		for _, arg := range args {
			if !arg.hasScalar || arg.scalar.value == nil {
				r.bashPPBuiltinError("TYPE", "%s arguments must all be ordered scalar values", name)
				return nil, false
			}
			argKind := arg.scalar.value.Kind()
			if argKind != constant.Int && argKind != constant.Float && argKind != constant.String {
				r.bashPPBuiltinError("TYPE", "%s arguments must all be ordered values", name)
				return nil, false
			}
			if kind == constant.Unknown {
				kind = argKind
			} else if kind == constant.String || argKind == constant.String {
				if kind != argKind {
					r.bashPPBuiltinError("TYPE", "%s arguments must all be ordered values of one kind", name)
					return nil, false
				}
			} else if argKind == constant.Float {
				kind = constant.Float
			}
			if arg.typ != nil {
				if resultType == nil {
					resultType = arg.typ
				} else if !r.bashPPTypeAssignable(arg.typ, resultType) || !r.bashPPTypeAssignable(resultType, arg.typ) {
					r.bashPPBuiltinError("TYPE", "%s typed arguments must have one type", name)
					return nil, false
				}
			}
		}
		if resultType != nil {
			named, ok := r.bashPPUnderlyingType(resultType).(*syntax.BashPPNamedType)
			if !ok {
				r.bashPPBuiltinError("TYPE", "%s result type must be an ordered scalar type", name)
				return nil, false
			}
			for _, arg := range args {
				if arg.typ == nil {
					if _, err := r.bashPPConvertScalar(named.Name.Value, arg.scalar); err != nil {
						r.bashPPBuiltinError("TYPE", "%v", err)
						return nil, false
					}
				}
			}
		}
		for i := 1; i < len(args); i++ {
			op := token.LSS
			if name == "max" {
				op = token.GTR
			}
			if constant.Compare(args[i].scalar.value, op, args[best].scalar.value) {
				best = i
			}
		}
		result := bashPPBuiltinScalarCell(bashPPScalarString(args[best].scalar.value))
		result.scalarKind = args[best].scalar.value.Kind()
		result.declType = resultType
		if named, ok := resultType.(*syntax.BashPPNamedType); ok {
			result.typeName = named.Name.Value
		}
		return result, true

	case "print", "println":
		if c.Ellipsis.IsValid() {
			r.bashPPBuiltinError("ARITY", "%s does not accept a spread argument", name)
			return nil, false
		}
		parts := make([]string, len(args))
		for i, arg := range args {
			if r.bashPPGoSource && arg.hasScalar {
				parts[i] = r.goSourcePrintScalar(arg.scalar)
			} else {
				parts[i] = bashPPBuiltinScalar(arg.value)
			}
		}
		if r.bashPPGoSource {
			if name == "println" {
				r.errf("%s\n", strings.Join(parts, " "))
			} else {
				r.errf("%s", strings.Join(parts, ""))
			}
		} else if name == "println" {
			r.outf("%s\n", strings.Join(parts, " "))
		} else {
			r.outf("%s", strings.Join(parts, ""))
		}
		return nil, false

	case "new":
		r.bashPPBuiltinError("TYPE", "new requires exactly one type argument and is only a value expression")
		return nil, false
	}
	return nil, false
}

func (r *Runner) bashPPBindBuiltinResult(d *syntax.BashPPShortDecl, result *bashPPCell, produced bool) {
	if !produced {
		if r.exit.code == 0 {
			r.bashPPBuiltinError("ARITY", "%s produces no value", d.Call.Fun[0].Value)
		}
		return
	}
	if len(d.Lhs) != 1 {
		r.errf("assignment mismatch: %d variable(s) but 1 value(s)\n", len(d.Lhs))
		r.exit = exitStatus{code: 2}
		return
	}
	name := d.Lhs[0].Value
	r.bashPPDeclareName(name, result.vr)
	if r.exit.code != 0 {
		return
	}
	cell := r.bashPPScope.lookup(name)
	cell.publish(result)
	// One snapshot of what publish() just stored: the identity pointer, the
	// value kind and the collection meta are the same bundle, and naming an
	// owner through the snapshot's pointer is the same store.
	if stored := cell.view(); stored.object == nil && stored.vr.Kind == expand.Object {
		cell.object = &bashPPObjectIdentity{owner: name, collection: stored.valueMeta}
	} else if stored.object != nil && stored.object.owner == "" {
		stored.object.owner = name
	}
}

// bashPPBuiltinLength is shared by statement calls and positioned scalar calls.
// It returns diagnostics instead of printing, so either caller reports once.
func (r *Runner) bashPPBuiltinLength(name string, c *syntax.BashPPCall, args []bashPPBuiltinArg) (*bashPPCell, error) {
	if len(args) != 1 || c.Ellipsis.IsValid() {
		return nil, fmt.Errorf("BASHPP-EBUILTIN-ARITY: %s expects exactly 1 argument; got %d argument(s)", name, len(args))
	}
	arg := args[0]
	// Resident channels in aggregates carry identity in metadata, not text.
	if r.bashPPGoSource && arg.channel == nil && arg.cell != nil && arg.cell.vr.Kind == expand.String && arg.cell.vr.Str == "" {
		if _, ok := r.bashPPUnderlyingType(arg.typ).(*syntax.BashPPChanType); ok {
			return bashPPBuiltinScalarCell("0"), nil
		}
	}
	if arg.channel != nil {
		if r.bashPPConcurrent == nil || arg.cell.channelOwner != r.bashPPConcurrent || r.bashPPChanBoundary {
			return nil, fmt.Errorf("BASHPP-EBUILTIN-TYPE: %s argument is not a channel in this task group", name)
		}
		size := len(arg.channel.ch)
		if name == "cap" {
			size = cap(arg.channel.ch)
		}
		return bashPPBuiltinScalarCell(strconv.Itoa(size)), nil
	}
	if size, ok := r.bashPPBuiltinPointerArrayLen(arg); ok {
		return bashPPBuiltinScalarCell(strconv.Itoa(size)), nil
	}
	// A dependency-owned value answers its own length; see
	// bashPPNativeBuiltinLength in bashpp_native_access.go.
	if size, err, native := r.bashPPNativeBuiltinLength(name, c, arg); native {
		if err != nil {
			return nil, err
		}
		return bashPPBuiltinScalarCell(strconv.Itoa(size)), nil
	}
	if name == "len" {
		if text, ok := arg.value.(string); ok && arg.meta == nil {
			return bashPPBuiltinScalarCell(strconv.Itoa(len(text))), nil
		}
	}
	if arg.value == nil && arg.meta == nil {
		return nil, fmt.Errorf("BASHPP-EBUILTIN-NIL: %s cannot be applied to untyped nil", name)
	}
	kinds := []string{"array", "inferred-array", "slice", "map"}
	if name == "cap" {
		kinds = kinds[:3]
	}
	if _, ok := r.bashPPBuiltinCollection(arg, kinds...); !ok {
		return nil, fmt.Errorf("BASHPP-EBUILTIN-TYPE: %s argument must be %s", name, map[bool]string{true: "an array or slice", false: "a string, array, slice, or map"}[name == "cap"])
	}
	if arg.meta.kind == "map" {
		// A nil map is a map with no entries, so its payload is absent rather
		// than an empty table. Reading it as a table crashed the interpreter on
		// `len(m)` for `var m map[string]int`.
		table, _ := arg.value.(map[string]any)
		return bashPPBuiltinScalarCell(strconv.Itoa(bashPPStorageLen(table))), nil
	}
	seq, _ := arg.value.([]any)
	size := bashPPSequenceLen(arg.meta, seq)
	if name == "cap" {
		size = bashPPSequenceCap(arg.meta, seq)
	}
	return bashPPBuiltinScalarCell(strconv.Itoa(size)), nil
}
