// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"errors"
	"fmt"
	"go/constant"
	"iter"
	"strconv"
	"strings"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

var errBashPPTypeCycle = errors.New("cyclic type representation")

func cloneBashPPTypeSet(src map[string]bool) map[string]bool {
	dst := make(map[string]bool, len(src))
	for key := range src {
		dst[key] = true
	}
	return dst
}

// bashPPValidateTypeRepresentation follows instantiated named types while
// retaining two pieces of state: all active instantiations (to terminate legal
// recursive paths), and the active instantiations since the last representation
// boundary. Re-entering the latter is an infinite value layout. Pointers,
// slices, and maps reset that direct set because their headers have finite size;
// arrays and structs deliberately do not.
func (r *Runner) bashPPValidateTypeRepresentation(typ syntax.BashPPTypeExpr, active, direct map[string]bool) error {
	switch x := typ.(type) {
	case *syntax.BashPPChanType:
		if r.bashPPGoSource {
			return r.bashPPValidateTypeRepresentation(x.Element, active, make(map[string]bool))
		}
		return fmt.Errorf("BASHPP-ESTRUCT-FIELD-TYPE: unsupported field type %s", bashPPTypeText(typ))
	case *syntax.BashPPTypeParamType:
		return nil
	case *syntax.BashPPUnionType, *syntax.BashPPApproxType:
		return fmt.Errorf("BASHPP-EGENERIC-ARG: %s is a constraint expression, not a concrete type", bashPPTypeText(typ))
	case *syntax.BashPPNamedType:
		if err := bashPPValidateConcreteTypeArgs(x.TypeArgs); err != nil {
			return err
		}
		name := x.Name.Value
		// comparable is a predeclared constraint interface. It may be the
		// underlying type of a declared constraint (type C comparable), but it
		// is not a concrete builtin type and must stay out of bashPPBuiltinType.
		if name == "comparable" {
			return nil
		}
		if r.bashPPNativeType(x) {
			_, err := r.bashPPNativeTypeRequest("type", x)
			return err
		}
		if bashPPScalarType(name) {
			return nil
		}
		_, ok := r.bashPPTypes[name]
		if !ok {
			if name == "error" || name == "any" {
				return nil
			}
			return fmt.Errorf("undefined type: %s", name)
		}
		if err := r.bashPPValidateNamedTypeArgs(x); err != nil {
			return err
		}
		// Declaration identity, rather than full instantiation text, makes the
		// traversal terminate even for expanding recursion such as A[*T]. The
		// representation boundary still decides whether that recurrence is legal.
		key := name
		if active[key] {
			if direct[key] {
				return errBashPPTypeCycle
			}
			return nil
		}
		active[key] = true
		defer delete(active, key)
		nextDirect := cloneBashPPTypeSet(direct)
		nextDirect[key] = true
		return r.bashPPValidateTypeRepresentation(r.bashPPInstantiateNamedType(x), active, nextDirect)
	case *syntax.BashPPPointerType:
		if x.Element == nil {
			return fmt.Errorf("BASHPP-EPOINTER-TYPE: invalid pointer type %s", bashPPTypeText(typ))
		}
		return r.bashPPValidateTypeRepresentation(x.Element, active, make(map[string]bool))
	case *syntax.BashPPCollectionType:
		if x.Kind == "array" {
			if x.Length == nil {
				return fmt.Errorf("BASHPP-ECOLLECTION-LENGTH: array length is missing")
			}
			n, err := r.bashPPArrayLength(x.Length.Value)
			if err != nil || n < 0 {
				return fmt.Errorf("BASHPP-ECOLLECTION-LENGTH: invalid array length %s", x.Length.Value)
			}
			return r.bashPPValidateTypeRepresentation(x.Element, active, direct)
		}
		if x.Kind == "map" {
			if !r.bashPPMapKeyType(x.Key) {
				return fmt.Errorf("BASHPP-ECOLLECTION-KEY: unsupported map key type %s", bashPPTypeText(x.Key))
			}
			if err := r.bashPPValidateTypeRepresentation(x.Key, active, make(map[string]bool)); err != nil {
				return err
			}
		}
		return r.bashPPValidateTypeRepresentation(x.Element, active, make(map[string]bool))
	case *syntax.BashPPStructType:
		seenFields := make(map[string]bool)
		for _, field := range x.Fields {
			if field.Embedded {
				if _, ok := bashPPEmbeddedFieldName(field); !ok {
					return fmt.Errorf("BASHPP-ESTRUCT-EMBED: unsupported embedded field type %s", bashPPTypeText(field.FieldTypeExpr))
				}
				_, indirect, definedPointer, iface := r.bashPPEmbeddedTarget(field.FieldTypeExpr)
				if definedPointer {
					name, _ := bashPPEmbeddedFieldName(field)
					return fmt.Errorf("BASHPP-ESTRUCT-EMBED: defined pointer type %s cannot be embedded", name)
				}
				if indirect && iface {
					return fmt.Errorf("BASHPP-ESTRUCT-EMBED: pointer to interface type %s cannot be embedded", bashPPTypeText(field.FieldTypeExpr))
				}
			}
		}
		for _, field := range bashPPFlatFields(x.Fields) {
			if seenFields[field.name] && (!r.bashPPGoSource || field.name != "_") {
				return fmt.Errorf("BASHPP-ESTRUCT-FIELD-DUPLICATE: field %q declared more than once", field.name)
			}
			seenFields[field.name] = true
			if err := r.bashPPValidateTypeRepresentation(field.typ, active, direct); err != nil {
				return err
			}
		}
		return nil
	case *syntax.BashPPFuncType:
		for _, fields := range [][]*syntax.BashPPField{x.Params, x.Results} {
			for _, field := range fields {
				if err := r.bashPPValidateTypeRepresentation(field.FieldTypeExpr, active, make(map[string]bool)); err != nil {
					return err
				}
			}
		}
		return nil
	case *syntax.BashPPInterfaceType:
		if r.bashPPInterfaceHasTypeTerms(x, make(map[*syntax.BashPPInterfaceType]bool)) {
			return fmt.Errorf("BASHPP-EINTERFACE-TYPESET: constraint interface cannot be used as a value type")
		}
		return r.bashPPValidateInterfaceType("anonymous", x)
	}
	return fmt.Errorf("BASHPP-ESTRUCT-FIELD-TYPE: unsupported field type %s", bashPPTypeText(typ))
}

func (r *Runner) bashPPValidateValueType(typ syntax.BashPPTypeExpr, seen map[string]bool) error {
	direct := cloneBashPPTypeSet(seen)
	return r.bashPPValidateTypeRepresentation(typ, seen, direct)
}

func (r *Runner) bashPPStructFields(typ syntax.BashPPTypeExpr) ([]*syntax.BashPPField, string, bool) {
	switch x := typ.(type) {
	case *syntax.BashPPStructType:
		return x.Fields, "struct", true
	case *syntax.BashPPNamedType:
		owner := x.Name.Value
		current := x
		seen := make(map[string]bool)
		for current != nil && !seen[bashPPTypeText(current)] {
			seen[bashPPTypeText(current)] = true
			_, ok := r.bashPPTypes[current.Name.Value]
			if !ok {
				return nil, "", false
			}
			instantiated := r.bashPPInstantiateNamedType(current)
			if st, ok := instantiated.(*syntax.BashPPStructType); ok {
				return st.Fields, owner, true
			}
			current, _ = instantiated.(*syntax.BashPPNamedType)
		}
	}
	return nil, "", false
}

func bashPPFieldType(fields []*syntax.BashPPField, name string) (syntax.BashPPTypeExpr, bool) {
	for _, field := range fields {
		if embedded, ok := bashPPEmbeddedFieldName(field); ok && embedded == name {
			return field.FieldTypeExpr, true
		}
		for _, fieldName := range field.Names {
			if fieldName.Value == name {
				return field.FieldTypeExpr, true
			}
		}
	}
	return nil, false
}

// bashPPFieldSequence walks the immutable declaration without allocating a
// flattened schema for each value encoded at the bridge boundary.
func bashPPFieldSequence(fields []*syntax.BashPPField) iter.Seq2[string, syntax.BashPPTypeExpr] {
	return func(yield func(string, syntax.BashPPTypeExpr) bool) {
		for _, field := range fields {
			if name, ok := bashPPEmbeddedFieldName(field); ok {
				if !yield(name, field.FieldTypeExpr) {
					return
				}
				continue
			}
			for _, name := range field.Names {
				if !yield(name.Value, field.FieldTypeExpr) {
					return
				}
			}
		}
	}
}

func bashPPFlatFields(fields []*syntax.BashPPField) []struct {
	name string
	typ  syntax.BashPPTypeExpr
} {
	var out []struct {
		name string
		typ  syntax.BashPPTypeExpr
	}
	for _, field := range fields {
		if name, ok := bashPPEmbeddedFieldName(field); ok {
			out = append(out, struct {
				name string
				typ  syntax.BashPPTypeExpr
			}{name, field.FieldTypeExpr})
			continue
		}
		for _, name := range field.Names {
			out = append(out, struct {
				name string
				typ  syntax.BashPPTypeExpr
			}{name.Value, field.FieldTypeExpr})
		}
	}
	return out
}

func (r *Runner) bashPPEvalComposite(lit *syntax.BashPPCompositeLit, expected syntax.BashPPTypeExpr) (any, *bashPPCollectionMeta, error) {
	typ := lit.LitType
	if typ == nil {
		typ = expected
	}
	if _, ok := r.bashPPUnderlyingType(typ).(*syntax.BashPPCollectionType); ok {
		return r.bashPPEvalCollection(lit, expected)
	}
	fields, typeName, ok := r.bashPPStructFields(typ)
	importedHeader := false
	if !ok && r.bashPPGoSource {
		// Imported structs normally remain dependency-owned. A slice-header
		// literal is different: the program has just built its three words,
		// and an unsafe conversion immediately needs that local storage.
		// Restrict the exception to the authenticated header shape.
		if header, headerOK := r.goSourceUnsafeSliceHeaderFields(typ); headerOK {
			fields, typeName, ok = header, bashPPTypeText(typ), true
			importedHeader = true
		}
	}
	if !ok {
		return nil, nil, fmt.Errorf("BASHPP-ESTRUCT-TYPE: %s is not a supported struct type", bashPPTypeText(typ))
	}
	// A type alias is the very type it names, so `Eint{…}` is an `E[int]`
	// literal where the field was declared `E[int]`, and vice versa. Resolve
	// each side's aliases before comparing; two distinct defined types stay
	// distinct because alias resolution stops at the first non-alias name.
	if expected != nil && lit.LitType != nil &&
		bashPPTypeText(r.bashPPCanonicalAssignableType(lit.LitType)) != bashPPTypeText(r.bashPPCanonicalAssignableType(expected)) {
		return nil, nil, fmt.Errorf("BASHPP-ESTRUCT-TYPE: cannot use %s as %s", bashPPTypeText(lit.LitType), bashPPTypeText(expected))
	}
	flat := bashPPFlatFields(fields)
	out := make(map[string]any, len(flat))
	meta := &bashPPCollectionMeta{kind: "struct", typ: typ}
	for _, field := range flat {
		value, child := r.bashPPZeroValue(field.typ)
		bashPPSetInitialStructField(out, meta, field.name, value, child)
	}
	keyed, positional := false, false
	for _, elem := range lit.Elems {
		keyed = keyed || elem.Key != nil
		positional = positional || elem.Key == nil
	}
	if keyed && positional {
		return nil, nil, fmt.Errorf("BASHPP-ESTRUCT-MIXED: %s literal cannot mix keyed and positional fields", typeName)
	}
	if positional {
		if len(lit.Elems) != len(flat) {
			return nil, nil, fmt.Errorf("BASHPP-ESTRUCT-POSITIONAL: %s literal requires %d fields; got %d", typeName, len(flat), len(lit.Elems))
		}
		for i, elem := range lit.Elems {
			value, child, err := r.bashPPEvalTypedValue(elem.Value, flat[i].typ)
			if err != nil {
				return nil, nil, err
			}
			// Blank fields consume and evaluate their positional initializer,
			// but retain the zero value rather than the result.
			if r.bashPPGoSource && flat[i].name == "_" {
				continue
			}
			bashPPSetInitialStructField(out, meta, flat[i].name, value, child)
		}
		return out, meta, nil
	}
	if importedHeader {
		// The imported header's fields come from go/types metadata, not the
		// interpreted type registry the selector resolver walks. Its shape is
		// already authenticated as three flat words, so key them by name.
		seen := make(map[string]bool, len(lit.Elems))
		for _, elem := range lit.Elems {
			key, ok := elem.Key.(*syntax.BashPPIdent)
			if !ok {
				return nil, nil, fmt.Errorf("%sBASHPP-ESTRUCT-KEY: %s literal field key must be an identifier, not a selector expression", r.bashErrPrefix(elem.Key.Pos()), typeName)
			}
			name := key.Name.Value
			var fieldType syntax.BashPPTypeExpr
			for _, field := range flat {
				if field.name == name {
					fieldType = field.typ
				}
			}
			if fieldType == nil {
				return nil, nil, fmt.Errorf("%sBASHPP-ESTRUCT-UNKNOWN: %s has no field selector %q", r.bashErrPrefix(key.Pos()), typeName, name)
			}
			if seen[name] {
				return nil, nil, fmt.Errorf("%sBASHPP-ESTRUCT-DUPLICATE: field selector %q is supplied more than once", r.bashErrPrefix(key.Pos()), name)
			}
			seen[name] = true
			value, child, err := r.bashPPEvalTypedValue(elem.Value, fieldType)
			if err != nil {
				return nil, nil, err
			}
			bashPPSetInitialStructField(out, meta, name, value, child)
		}
		return out, meta, nil
	}
	seen := make(map[string]bool, len(lit.Elems))
	selections := make([]bashPPSelection, len(lit.Elems))
	for i, elem := range lit.Elems {
		key, ok := elem.Key.(*syntax.BashPPIdent)
		if !ok {
			return nil, nil, fmt.Errorf("%sBASHPP-ESTRUCT-KEY: %s literal field key must be an identifier, not a selector expression", r.bashErrPrefix(elem.Key.Pos()), typeName)
		}
		name := key.Name.Value
		sel := r.bashPPResolveField(typ, name)
		if sel.ambiguous {
			return nil, nil, fmt.Errorf("%sBASHPP-ESTRUCT-AMBIGUOUS: field selector %s.%s is ambiguous", r.bashErrPrefix(key.Pos()), typeName, name)
		}
		if len(sel.edges) == 0 {
			return nil, nil, fmt.Errorf("%sBASHPP-ESTRUCT-UNKNOWN: %s has no field selector %q", r.bashErrPrefix(key.Pos()), typeName, name)
		}
		if seen[name] {
			return nil, nil, fmt.Errorf("%sBASHPP-ESTRUCT-DUPLICATE: field selector %q is supplied more than once", r.bashErrPrefix(key.Pos()), name)
		}
		seen[name] = true
		for _, edge := range sel.edges[:len(sel.edges)-1] {
			if edge.pointer {
				return nil, nil, fmt.Errorf("%sBASHPP-ESTRUCT-KEY-POINTER: field selector %s.%s traverses pointer field %s", r.bashErrPrefix(key.Pos()), typeName, name, edge.name)
			}
		}
		for previous := 0; previous < i; previous++ {
			if bashPPEmbeddedKeyConflict(selections[previous].edges, sel.edges) {
				return nil, nil, fmt.Errorf("%sBASHPP-ESTRUCT-KEY-CONFLICT: field selector %s.%s conflicts with key %s", r.bashErrPrefix(key.Pos()), typeName, name, bashPPEmbedPath(selections[previous].edges))
			}
		}
		selections[i] = sel
	}
	for i, elem := range lit.Elems {
		sel := selections[i]
		value, child, err := r.bashPPEvalTypedValue(elem.Value, sel.fieldType)
		if err != nil {
			return nil, nil, err
		}
		if err := bashPPSetStructSelector(out, meta, sel.edges, value, child); err != nil {
			return nil, nil, fmt.Errorf("%sBASHPP-ESTRUCT-KEY-STORAGE: %v", r.bashErrPrefix(elem.Key.Pos()), err)
		}
	}
	return out, meta, nil
}

func bashPPEmbeddedKeyConflict(left, right []bashPPEmbedEdge) bool {
	shorter, longer := left, right
	if len(shorter) > len(longer) {
		shorter, longer = longer, shorter
	}
	if len(shorter) == len(longer) {
		return false
	}
	for i := range shorter {
		if shorter[i].name != longer[i].name {
			return false
		}
	}
	return true
}

func bashPPEmbedPath(edges []bashPPEmbedEdge) string {
	parts := make([]string, len(edges))
	for i, edge := range edges {
		parts[i] = edge.name
	}
	return strings.Join(parts, ".")
}

func bashPPSetStructSelector(root map[string]any, meta *bashPPCollectionMeta, edges []bashPPEmbedEdge, value any, child *bashPPCollectionMeta) error {
	mapping := root
	currentMeta := meta
	for i, edge := range edges {
		if i == len(edges)-1 {
			bashPPStorageSetField(mapping, currentMeta.mapping, edge.name, value, child)
			return nil
		}
		nestedValue, _ := bashPPStorageGet(mapping, edge.name)
		nested, ok := nestedValue.(map[string]any)
		if !ok || currentMeta == nil {
			return fmt.Errorf("promoted path %s no longer names struct storage", bashPPEmbedPath(edges[:i+1]))
		}
		nestedMeta := bashPPLayoutGet(currentMeta.mapping, edge.name)
		if nestedMeta == nil || nestedMeta.kind != "struct" {
			return fmt.Errorf("promoted path %s no longer names struct storage", bashPPEmbedPath(edges[:i+1]))
		}
		mapping, currentMeta = nested, nestedMeta
	}
	return fmt.Errorf("empty field selector")
}

// bashPPSetInitialStructField builds private struct storage. Scalar fields have
// no runtime shape beyond their declared type, so omitting their nil layout
// entries avoids a second hash map on the common all-scalar struct. A nil
// child still clears a layout entry the zero-value pre-fill left behind —
// a positional initializer carrying no metadata of its own (a closure handle
// for a func-typed field) must not keep reading under the zero's layout or
// the value and its metadata disagree about what the field holds.
func bashPPSetInitialStructField(out map[string]any, meta *bashPPCollectionMeta, name string, value any, child *bashPPCollectionMeta) {
	out[name] = value
	if child == nil {
		delete(meta.mapping, name)
		return
	}
	if meta.mapping == nil {
		meta.mapping = make(map[string]*bashPPCollectionMeta)
	}
	meta.mapping[name] = child
}

func (r *Runner) bashPPZeroValue(typ syntax.BashPPTypeExpr) (any, *bashPPCollectionMeta) {
	if value, meta, ok := r.goSourceNilCallableOrChannel(typ); ok {
		return value, meta
	}
	if r.bashPPNativeType(typ) {
		value, err := r.bashPPNativeTypeRequest("new", typ)
		if err != nil {
			r.exit.fatal(err)
			return nil, nil
		}
		if scalar, meta, ok, err := r.goSourceNativeAssignedScalar(value, typ); ok {
			if err != nil {
				r.exit.fatal(err)
				return nil, nil
			}
			return scalar, meta
		}
		return &value, &bashPPCollectionMeta{kind: "native", typ: typ}
	}
	if _, ok := r.bashPPInterfaceType(typ); ok {
		return "", &bashPPCollectionMeta{kind: "interface", typ: typ, interfaceValue: &bashPPInterfaceValue{nilIface: true}}
	}
	if fields, _, ok := r.bashPPStructFields(typ); ok {
		out := make(map[string]any)
		meta := &bashPPCollectionMeta{kind: "struct", typ: typ}
		for _, field := range bashPPFlatFields(fields) {
			value, child := r.bashPPZeroValue(field.typ)
			bashPPSetInitialStructField(out, meta, field.name, value, child)
		}
		return out, meta
	}
	if _, ok := r.bashPPPointerType(typ); ok {
		return nil, bashPPPointerMeta(typ)
	}
	return r.bashPPCollectionZero(typ)
}

// goSourceOpaqueUnsafeViewIdent reports an identifier holding an unsafe view
// pointer — (*reflect.SliceHeader)(unsafe.Pointer(p)) — assigned to an
// imported pointer destination. The pointer stays the interpreter's own
// storage until an access through it needs the layout (fixedbugs/issue8004:
// the header is only stored, compared and converted back), so it takes the
// pointer path of bashPPEvalTypedValue rather than the dependency bridge,
// which would materialize the view now and refuse a source it cannot lay out.
func (r *Runner) goSourceOpaqueUnsafeViewIdent(expr syntax.BashPPExpr, expected syntax.BashPPTypeExpr) bool {
	if _, ok := r.bashPPPointerType(expected); !ok {
		return false
	}
	id, ok := bashPPUnparenExpr(expr).(*syntax.BashPPIdent)
	if !ok {
		return false
	}
	cell := r.bashPPScope.lookup(id.Name.Value)
	if cell == nil {
		return false
	}
	cell = cell.view()
	return cell != nil && cell.pointer && cell.pointerValue != nil && cell.pointerValue.unsafeSource() != nil
}

func (r *Runner) bashPPEvalTypedValue(expr syntax.BashPPExpr, expected syntax.BashPPTypeExpr) (value any, meta *bashPPCollectionMeta, err error) {
	defer func() { err = r.goSourceRuntimeFaultAt(err, expr) }()
	if lit, ok := expr.(*syntax.BashPPCompositeLit); ok && r.bashPPNativeType(expected) {
		if _, header := r.goSourceUnsafeSliceHeaderFields(expected); header {
			return r.bashPPEvalComposite(lit, expected)
		}
		nativeLit := *lit
		nativeLit.LitType = expected
		value, err := r.bashPPNativeComposite(&nativeLit, false)
		if err != nil {
			return nil, nil, err
		}
		return &value, &bashPPCollectionMeta{kind: "native", typ: expected}, nil
	}
	if r.bashPPGoSource && r.bashPPNativeType(expected) && r.bashPPNativeExpr(expr) {
		value, err := r.bashPPBridgeExpr(expr)
		if err != nil {
			return nil, nil, err
		}
		assigned, meta, err := r.goSourceNativeAssignedValue(value, expected)
		if err != nil {
			return nil, nil, err
		}
		if scalar, scalarMeta, ok, err := r.goSourceNativeAssignedScalar(value, expected); ok {
			return scalar, scalarMeta, err
		}
		return assigned, meta, nil
	}
	if r.bashPPGoSource && r.bashPPNativeType(expected) && !r.goSourceOpaqueUnsafeViewIdent(expr, expected) {
		// A locally-held value can be assigned to an imported interface too.
		// Let the dependency authenticate that assignment so its static
		// interface wrapper survives the aggregate field. In particular, a
		// nil *strings.Reader stored in an io.Reader is a non-nil interface;
		// treating the imported destination as an ordinary native value loses
		// that distinction at a later comparison with nil.
		cell, err := r.goSourceValueCell(expr)
		if err != nil {
			return nil, nil, err
		}
		bridge, err := r.bashPPBridgeCell(cell)
		if err != nil {
			return nil, nil, err
		}
		assigned, meta, err := r.goSourceNativeAssignedValue(bridge, expected)
		if err != nil {
			return nil, nil, err
		}
		if scalar, scalarMeta, ok, err := r.goSourceNativeAssignedScalar(bridge, expected); ok {
			return scalar, scalarMeta, err
		}
		return assigned, meta, nil
	}
	if _, ok := r.bashPPInterfaceType(expected); ok {
		iv, vr, err := r.bashPPMakeInterfaceValue(expr, expected)
		if err != nil {
			return nil, nil, err
		}
		value := any(vr.String())
		if vr.Kind == expand.Object {
			value = vr.Obj
		}
		return value, &bashPPCollectionMeta{kind: "interface", typ: expected, interfaceValue: iv}, nil
	}
	// unsafe.Pointer is a named basic type to the Go type checker, rather than
	// a *T shape, but its runtime value still names interpreter storage. In a
	// typed aggregate slot such as reflect.SliceHeader.Data, route conversions
	// like unsafe.Pointer(&slice[0]) through the pointer evaluator before the
	// scalar fallback tries to evaluate the address expression arithmetically.
	if r.bashPPGoSource && r.goSourceUnsafePointerType(expected) {
		if goSourceNilLiteral(expr) {
			return nil, bashPPPointerMeta(expected), nil
		}
		ptr, err := r.bashPPPointerExprValue(expr)
		if err != nil {
			return nil, nil, err
		}
		return ptr, bashPPPointerMeta(expected), nil
	}
	if pointerType, ok := r.bashPPPointerType(expected); ok {
		if id, nilIdent := expr.(*syntax.BashPPIdent); nilIdent && id.Name.Value == "nil" {
			return nil, bashPPPointerMeta(expected), nil
		}
		ptr, err := r.bashPPPointerExprValue(expr)
		if err != nil {
			// A dependency's own pointer — parse(...) returning
			// *template.Template — is stored as its native handle, the same
			// way a native-typed expression would have been above.
			var native *bashPPNativePointerValueError
			if errors.As(err, &native) && (r.bashPPNativeType(expected) || r.goSourceNativeAddressExpr(expr)) {
				return r.goSourceNativeAssignedValue(*native.value, expected)
			}
			// A nil pointer the dependency minted — reflect.New(...).Elem()
			// of a pointer type, asserted back out of an interface — is the
			// nil pointer of the declared type; only a non-nil handle names
			// storage the interpreter cannot alias (bug510.go).
			if errors.As(err, &native) && native.value != nil {
				isNil, nilErr := r.goSourceNativeHandleIsNil(*native.value)
				if nilErr != nil {
					return nil, nil, nilErr
				}
				if isNil {
					return nil, bashPPPointerMeta(expected), nil
				}
			}
			return nil, nil, err
		}
		actual := r.bashPPPointerExprType(expr, ptr)
		if actual == nil {
			actual = &syntax.BashPPPointerType{Element: pointerType.Element}
		}
		if !r.bashPPTypeAssignable(actual, expected) {
			return nil, nil, fmt.Errorf("BASHPP-EASSIGN-MISMATCH: cannot use %s as %s", bashPPTypeText(actual), bashPPTypeText(expected))
		}
		return ptr, bashPPPointerMeta(expected), nil
	}
	if deref, ok := expr.(*syntax.BashPPDerefExpr); ok {
		ptr, err := r.bashPPPointerExprValue(deref.X)
		if err != nil {
			return nil, nil, err
		}
		if ptr == nil {
			return nil, nil, errBashPPNilDereference
		}
		value, meta, typ, err := ptr.read()
		if err != nil {
			return nil, nil, err
		}
		if expected != nil && bashPPTypeText(typ) != bashPPTypeText(expected) {
			return nil, nil, fmt.Errorf("BASHPP-EASSIGN-MISMATCH: cannot use %s as %s", bashPPTypeText(typ), bashPPTypeText(expected))
		}
		value, meta = bashPPCopyArrayValue(value, meta)
		return value, meta, nil
	}
	if lit, ok := expr.(*syntax.BashPPCompositeLit); ok {
		return r.bashPPEvalComposite(lit, expected)
	}
	if conv, ok := expr.(*syntax.BashPPConvertExpr); ok {
		if value, meta, handled, err := r.goSourceConvertedComposite(conv); handled {
			if err != nil {
				return nil, nil, err
			}
			if expected != nil && !r.bashPPTypeAssignable(meta.typ, expected) {
				return nil, nil, fmt.Errorf("BASHPP-EASSIGN-MISMATCH: cannot use %s as %s", bashPPTypeText(meta.typ), bashPPTypeText(expected))
			}
			return value, meta, nil
		}
	}
	if _, ok := expr.(*syntax.BashPPIndexExpr); ok {
		value, meta, err := r.bashPPReadExpr(expr)
		if err != nil {
			return nil, nil, err
		}
		value, meta, err = r.goSourceMaterializeTypedCollection(value, meta, expected)
		if err != nil {
			return nil, nil, err
		}
		if err := r.bashPPCheckTypedValue(value, meta, expected); err != nil {
			return nil, nil, err
		}
		value, meta = bashPPCopyArrayValue(value, meta)
		return value, meta, nil
	}
	if _, ok := expr.(*syntax.BashPPSliceExpr); ok {
		value, meta, err := r.bashPPReadExpr(expr)
		if err != nil {
			return nil, nil, err
		}
		value, meta, err = r.goSourceMaterializeTypedCollection(value, meta, expected)
		if err != nil {
			return nil, nil, err
		}
		if err := r.bashPPCheckTypedValue(value, meta, expected); err != nil {
			return nil, nil, err
		}
		return value, meta, nil
	}
	// A method value — `TearDown: res.teardown` filling a func field — is
	// the closure the element path binds, not a field to read. A method
	// expression — `one: (*state).constInt64` naming a type's method as a
	// function — is likewise the forwarding closure the element path binds:
	// reading its `(*state)` operand as a value would ask for a pointer that
	// the type name is not.
	if sel, ok := expr.(*syntax.BashPPSelectorExpr); ok && !(sel.MethodValue && r.bashPPGoSource && !r.bashPPNativeExpr(sel.X)) && !r.goSourceMethodExprSelector(sel) {
		value, meta, err := r.bashPPReadExpr(expr)
		if err != nil {
			return nil, nil, err
		}
		value, meta, err = r.goSourceMaterializeTypedCollection(value, meta, expected)
		if err != nil {
			return nil, nil, err
		}
		if err := r.bashPPCheckTypedValue(value, meta, expected); err != nil {
			return nil, nil, err
		}
		value, meta = bashPPCopyArrayValue(value, meta)
		return value, meta, nil
	}
	if id, ok := expr.(*syntax.BashPPIdent); ok {
		// The object kind, the carried value and the collection meta describe
		// ONE store, and everything below chooses among them — a materialized
		// carrier, a type check, an array copy. They come from one snapshot so
		// a rebinding cannot make the check and the copied value disagree.
		if cell := r.bashPPScope.lookup(id.Name.Value).view(); cell != nil && cell.vr.Kind == expand.Object {
			meta := bashPPCellMeta(cell)
			// A non-plain tuple assignment is lowered through result temporaries.
			// A dependency result in one of those cells is still a native carrier,
			// with no collection metadata of its own. Materialize that carrier only
			// when it is being stored in an interpreter-owned collection target;
			// imported/native targets keep their lazy handle and identity.
			if value, materializedMeta, err := r.goSourceMaterializeTypedCollection(cell.vr.Obj, meta, expected); err != nil {
				return nil, nil, err
			} else if materializedMeta != meta {
				value, materializedMeta = bashPPCopyArrayValue(value, materializedMeta)
				return value, materializedMeta, nil
			}
			if err := r.bashPPCheckTypedValue(cell.vr.Obj, meta, expected); err != nil {
				return nil, nil, err
			}
			value, copied := bashPPCopyArrayValue(cell.vr.Obj, meta)
			return value, copied, nil
		}
	}
	value, meta, err = r.bashPPEvalElement(expr, expected)
	if r.bashPPGoSource {
		return value, meta, err
	}
	return value, nil, err
}

// goSourceMaterializeTypedCollection turns a dependency-owned collection
// carrier into interpreter storage when it is assigned to an
// interpreter-owned collection destination. Selectors need this just as named
// result temporaries do: a native field read has no local collection metadata
// until its authenticated type is checked and its elements are transported.
func (r *Runner) goSourceMaterializeTypedCollection(value any, meta *bashPPCollectionMeta, expected syntax.BashPPTypeExpr) (any, *bashPPCollectionMeta, error) {
	if !r.bashPPGoSource || meta != nil || r.bashPPNativeType(expected) {
		return value, meta, nil
	}
	if _, collection := r.bashPPUnderlyingType(expected).(*syntax.BashPPCollectionType); !collection {
		return value, meta, nil
	}
	native, ok := value.(*bashPPBridgeValue)
	if !ok || native == nil {
		return value, meta, nil
	}
	converted, convertedMeta, handled, err := r.goSourceNativeSequenceContents(native, expected)
	if err != nil {
		return nil, nil, err
	}
	if handled {
		return converted, convertedMeta, nil
	}
	if native.Kind != "slice" && native.Kind != "array" && native.Kind != "map" {
		return value, meta, nil
	}
	if _, _, err := r.goSourceNativeAssignedValue(*native, expected); err != nil {
		return nil, nil, fmt.Errorf("BASHPP-ECOLLECTION-ELEMENT: %v", err)
	}
	converted, convertedMeta, err = r.bashPPBridgeContents(*native, expected)
	return converted, convertedMeta, err
}

func (r *Runner) bashPPCheckTypedValue(value any, meta *bashPPCollectionMeta, expected syntax.BashPPTypeExpr) error {
	if _, native := value.(*bashPPBridgeValue); native && r.bashPPNativeType(expected) {
		return r.goSourceCheckNativeElement(value, expected)
	}
	if iface, ok := r.bashPPInterfaceType(expected); ok {
		if meta == nil || meta.interfaceValue == nil {
			return fmt.Errorf("BASHPP-EASSIGN-MISMATCH: cannot use value as %s", bashPPTypeText(expected))
		}
		if meta.interfaceValue.nilIface {
			return nil
		}
		return r.bashPPImplements(meta.interfaceValue.dynamic, iface)
	}
	if _, ok := r.bashPPPointerType(expected); ok {
		if value == nil {
			return nil
		}
		if _, ok := value.(*bashPPPointer); !ok || meta == nil || !r.bashPPTypeAssignable(meta.typ, expected) {
			return fmt.Errorf("BASHPP-EASSIGN-MISMATCH: cannot use value as %s", bashPPTypeText(expected))
		}
		return nil
	}
	if _, ok := r.goSourceChannelType(expected); ok {
		return r.goSourceCheckChannelValue(value, meta, expected)
	}
	if _, _, ok := r.bashPPStructFields(expected); ok {
		if meta == nil || meta.kind != "struct" || !r.bashPPTypeAssignable(meta.typ, expected) {
			return fmt.Errorf("BASHPP-ESTRUCT-FIELD-TYPE: cannot use value as %s", bashPPTypeText(expected))
		}
		return nil
	}
	if _, ok := expected.(*syntax.BashPPCollectionType); ok {
		if meta == nil || !(r.bashPPTypeAssignable(meta.typ, expected) || r.bashPPInferredArrayAssignable(meta, expected)) {
			return fmt.Errorf("BASHPP-ECOLLECTION-ELEMENT: cannot use value as %s", bashPPTypeText(expected))
		}
		return nil
	}
	if _, ok := r.bashPPUnderlyingType(expected).(*syntax.BashPPCollectionType); ok {
		if meta == nil || !(r.bashPPTypeAssignable(meta.typ, expected) || r.bashPPInferredArrayAssignable(meta, expected)) {
			return fmt.Errorf("BASHPP-ECOLLECTION-ELEMENT: cannot use value as %s", bashPPTypeText(expected))
		}
		return nil
	}
	if meta != nil && meta.kind == "bridge-scalar" {
		if !r.bashPPTypeAssignable(meta.typ, expected) {
			return fmt.Errorf("BASHPP-ECOLLECTION-ELEMENT: cannot use %s value as %s", bashPPTypeText(meta.typ), bashPPTypeText(expected))
		}
		return nil
	}
	if err := r.bashPPCheckCollectionValue(value, expected); err != nil {
		// A dependency carrier reaching a scalar destination (a non-finite
		// float64 struct field, say) is still wrapped; validate it with the
		// same claim composite elements use instead of rejecting the wrapper.
		if _, _, claimed, bridgeErr := r.bashPPCollectionBridgeValue(value, expected); claimed {
			return bridgeErr
		}
		return err
	}
	return nil
}

func bashPPCellMeta(cell *bashPPCell) *bashPPCollectionMeta {
	if cell == nil {
		return nil
	}
	// Three alternative meta edges of one value; a shared cell is read from a
	// snapshot so a concurrent publish cannot make them disagree. See
	// bashpp_cell_share.go.
	cell = cell.view()
	if cell.pointer {
		return bashPPPointerMeta(cell.declType)
	}
	if cell.valueMeta != nil {
		return cell.valueMeta
	}
	if cell.object != nil {
		return cell.object.collection
	}
	return nil
}

func (r *Runner) bashPPReadExpr(expr syntax.BashPPExpr) (value any, meta *bashPPCollectionMeta, err error) {
	defer func() { err = r.goSourceRuntimeFaultAt(err, expr) }()
	// A call result is transported in a cell. In particular, pointers use the
	// cell's pointerValue side channel and intentionally have an empty scalar
	// spelling. Reading only vr below therefore turned every pointer-returning
	// call into an untyped empty scalar before a dereference or selector could
	// consume it.
	if r.bashPPGoSource {
		if value, meta, handled, err := r.goSourceUnsafeRead(expr); handled {
			return value, meta, err
		}
		if _, call := expr.(*syntax.BashPPCall); call {
			cell, err := r.goSourceValueCell(expr)
			if err != nil {
				return nil, nil, err
			}
			return r.bashPPReadCellValue(cell)
		}
	}
	if value, meta, handled, err := r.goSourceCollectionCallValue(expr, nil); handled {
		return value, meta, err
	}
	// An index or slice rooted in a dependency-owned value is read through its
	// handle; see bashPPNativeRead in bashpp_native_access.go.
	if value, meta, err, native := r.bashPPNativeRead(expr); native {
		return value, meta, err
	}
	switch x := expr.(type) {
	// Parentheses carry no value of their own. Every sibling walker --
	// bashPPAddress, bashPPCollectionRoot, bashPPPointerExprValue -- already
	// looks through them, so without this case a merely parenthesized operand
	// reached the fallthrough below and was reported as an unsupported
	// expression. `(*p)[k]` is the spelling Go requires whenever p is a pointer
	// to a defined map or slice type, since `p[k]` is not valid there.
	case *syntax.BashPPParenExpr:
		return r.bashPPReadExpr(x.X)
	case *syntax.BashPPAddressExpr, *syntax.BashPPNewExpr:
		pointer, err := r.bashPPPointerExprValue(expr)
		if err != nil {
			return nil, nil, err
		}
		return pointer, bashPPPointerMeta(r.bashPPPointerExprType(expr, pointer)), nil
	case *syntax.BashPPConvertExpr:
		if value, meta, handled, err := r.bashPPConvertToCollection(x); handled {
			return value, meta, err
		}
		if value, meta, handled, err := r.goSourceConvertedComposite(x); handled {
			return value, meta, err
		}
		if target, nilPointer := r.bashPPNilPointerConversion(x); nilPointer {
			return nil, bashPPPointerMeta(target), nil
		}
		if ptr, target, converted, err := r.bashPPPointerConversion(x); converted {
			if err != nil {
				return nil, nil, err
			}
			return ptr, bashPPPointerMeta(target), nil
		}
		scalar, err := r.bashPPEvalScalarExpr(x)
		if err != nil {
			return nil, nil, err
		}
		return bashPPScalarAny(scalar.value), nil, nil
	case *syntax.BashPPCompositeLit:
		return r.bashPPEvalComposite(x, nil)
	case *syntax.BashPPTypeAssertExpr:
		_, cell, err := r.bashPPTypeAssert(x, false)
		if err != nil {
			return nil, nil, err
		}
		return r.bashPPReadCellValue(cell)
	case *syntax.BashPPUnaryExpr:
		// `(<-c).(struct{})`, `m[(<-c).(T)]`: a receive in structured
		// position is the one receive the concurrency runtime performs,
		// and its value is the received cell — an interface value keeps
		// its dynamic type, a struct its layout. The scalar evaluator owns
		// every other unary operator; a receive that the runtime cancels
		// or deadlocks has already been reported, so it is the interrupt.
		if !r.bashPPGoSource || x.Op == nil || x.Op.Value != "<-" {
			break
		}
		cell, _ := r.bashPPReceiveCell(r.ectx, &syntax.BashPPReceive{Arrow: x.Pos(), ChanExpr: x.X}, nil)
		if cell == nil {
			return nil, nil, errBashPPScalarInterrupted
		}
		return r.bashPPReadCellValue(cell)
	case *syntax.BashPPIdent:
		// One snapshot: the pointer test, the object test and the payload
		// they select must all answer about the same store of a cell an
		// interpreted goroutine may be rebinding. See bashpp_cell_share.go.
		cell := r.bashPPScope.lookup(x.Name.Value).view()
		if cell != nil && cell.pointer {
			return cell.pointerValue, bashPPPointerMeta(cell.declType), nil
		}
		if cell == nil || cell.vr.Kind != expand.Object {
			return nil, nil, fmt.Errorf("BASHPP-ESELECTOR-ROOT: %s is not a structured value", x.Name.Value)
		}
		return cell.vr.Obj, bashPPCellMeta(cell), nil
	case *syntax.BashPPDerefExpr:
		ptr, err := r.bashPPPointerExprValue(x.X)
		if err != nil {
			if value, handled, nativeErr := r.goSourceNativePointee(err); handled {
				return value, nil, nativeErr
			}
			return nil, nil, err
		}
		if ptr == nil {
			return nil, nil, errBashPPNilDereference
		}
		value, meta, _, err := ptr.read()
		return value, meta, err
	case *syntax.BashPPSelectorExpr:
		if r.goSourceNilInterfaceOperand(x.X) {
			return nil, nil, errBashPPNilDereference
		}
		value, meta, err := r.bashPPReadExpr(x.X)
		if err != nil {
			return nil, nil, err
		}
		// A nil pointer field reads back as nil under pointer metadata.
		if value == nil && meta != nil && meta.kind == "pointer" {
			return nil, nil, errBashPPNilDereference
		}
		if pointer, ok := value.(*bashPPPointer); ok {
			if pointer == nil {
				return nil, nil, errBashPPNilDereference
			}
			value, meta, _, err = pointer.read()
			if err != nil {
				return nil, nil, err
			}
		}
		// A dependency-owned aggregate reached through an expression that is
		// not a named local — `caller().frame.Function`, where frame is a
		// runtime.Frame — keeps its fields in the worker. The named-local
		// spelling of the same read already goes through bashPPNativeRead;
		// this is the same member read for the value just produced.
		if native, ok := value.(*bashPPBridgeValue); ok && native != nil {
			member, err := r.bashPPNativeAccess(r.ectx, "member", *native, x.Sel.Value)
			if err != nil {
				return nil, nil, err
			}
			// An interface-typed member keeps its dynamic type, exactly as
			// the named-local spelling does through bashPPNativeRead.
			result, resultMeta, err := r.bashPPNativeValueRead(member)
			return result, resultMeta, err
		}
		if meta == nil || meta.kind != "struct" {
			return nil, nil, fmt.Errorf("BASHPP-ESELECTOR-TYPE: %s has no fields", bashPPExprText(x.X))
		}
		mapping, ok := value.(map[string]any)
		if !ok {
			return nil, nil, fmt.Errorf("BASHPP-ESELECTOR-TYPE: %s has no fields", bashPPExprText(x.X))
		}
		sel := r.bashPPResolveField(meta.typ, x.Sel.Value)
		if sel.ambiguous || len(sel.edges) == 0 {
			return nil, nil, bashPPSelectionError(meta.typ, x.Sel.Value, sel)
		}
		_ = mapping
		return bashPPReadSelection(value, meta, sel.edges)
	case *syntax.BashPPIndexExpr:
		if r.bashPPGoSource && x.GoString {
			scalar, err := r.bashPPEvalScalarExpr(x)
			if err != nil {
				return nil, nil, err
			}
			return bashPPScalarAny(scalar.value), nil, nil
		}
		value, meta, err := r.bashPPReadExpr(x.X)
		if err != nil {
			return nil, nil, err
		}
		if pointer, ok := value.(*bashPPPointer); ok {
			if pointer == nil {
				return nil, nil, errBashPPNilDereference
			}
			value, meta, _, err = pointer.read()
			if err != nil {
				return nil, nil, err
			}
		}
		// A dependency-owned collection reached through a computed base — a
		// member of a type-assertion result, say — materialises here as its
		// handle, which the static claim in bashPPNativeReadExpr cannot see.
		// Index it through the dependency, exactly as the named-local
		// spelling of the same read would.
		if native, ok := value.(*bashPPBridgeValue); ok && native != nil && r.bashPPGoSource {
			index, err := r.bashPPBridgeExpr(x.Index)
			if err != nil {
				return nil, nil, err
			}
			element, err := r.bashPPNativeAccess(r.ectx, "index", *native, "", index)
			if err != nil {
				return nil, nil, err
			}
			return r.bashPPNativeValueRead(element)
		}
		if meta == nil || meta.kind == "struct" {
			return nil, nil, fmt.Errorf("BASHPP-ECOLLECTION-INDEX: value is not a collection")
		}
		collection, ok := r.bashPPUnderlyingType(meta.typ).(*syntax.BashPPCollectionType)
		if !ok {
			return nil, nil, fmt.Errorf("BASHPP-ECOLLECTION-INDEX: value is not a collection")
		}
		if meta.kind == "map" {
			key, keyMeta, keyErr := r.bashPPEvalElement(x.Index, collection.Key)
			if keyErr != nil {
				return nil, nil, fmt.Errorf("BASHPP-ECOLLECTION-KEY: %v", keyErr)
			}
			mapping, valid := value.(map[string]any)
			if !valid && value != nil {
				return nil, nil, fmt.Errorf("BASHPP-ECOLLECTION-STORAGE: map payload has type %T", value)
			}
			storage, _, found, keyErr := r.bashPPSprint165MapLookup(meta, key, keyMeta, collection.Key)
			if keyErr != nil {
				return nil, nil, keyErr
			}
			result, child := bashPPSprint165MapEntryValueFound(mapping, meta, storage, found)
			if !found {
				zero, child := r.bashPPZeroValue(collection.Element)
				return zero, child, nil
			}
			return result, child, nil
		}
		index, indexErr := r.bashPPCollectionIndex(x.Index)
		if indexErr != nil {
			return nil, nil, indexErr
		}
		sequence, valid := value.([]any)
		if !valid && value != nil {
			return nil, nil, fmt.Errorf("BASHPP-ECOLLECTION-STORAGE: sequence payload has type %T", value)
		}
		if index.outOfBounds(len(sequence)) {
			if r.bashPPGoSource {
				return nil, nil, r.bashPPSprint162CollectionBoundsPanic(x, index, len(sequence))
			}
			return nil, nil, fmt.Errorf("BASHPP-ECOLLECTION-BOUNDS: index %s out of bounds for length %d", index.text, len(sequence))
		}
		i := index.value
		if i >= len(meta.sequence) {
			return nil, nil, fmt.Errorf("BASHPP-ECOLLECTION-STORAGE: missing element metadata")
		}
		return sequence[i], meta.sequence[i], nil
	case *syntax.BashPPSliceExpr:
		if r.bashPPGoSource && x.GoString {
			scalar, err := r.bashPPEvalScalarExpr(x)
			if err != nil {
				return nil, nil, err
			}
			return bashPPScalarAny(scalar.value), nil, nil
		}
		value, meta, err := r.bashPPReadExpr(x.X)
		if err != nil {
			return nil, nil, err
		}
		if pointer, ok := value.(*bashPPPointer); ok {
			if pointer == nil {
				return nil, nil, errBashPPNilDereference
			}
			value, meta, _, err = pointer.read()
			if err != nil {
				return nil, nil, err
			}
		} else if value == nil && meta != nil && meta.kind == "pointer" {
			// A nil pointer-to-array operand that is not a stored cell, such
			// as the conversion `(*[1]int)(nil)`, carries only pointer
			// metadata; slicing through it is Go's nil dereference.
			return nil, nil, errBashPPNilDereference
		}
		if meta == nil || meta.kind == "struct" || meta.kind == "map" {
			return nil, nil, fmt.Errorf("BASHPP-ECOLLECTION-SLICE: value is not sliceable")
		}
		collection, ok := r.bashPPUnderlyingType(meta.typ).(*syntax.BashPPCollectionType)
		if !ok {
			return nil, nil, fmt.Errorf("BASHPP-ECOLLECTION-SLICE: value is not sliceable")
		}
		sequence, ok := value.([]any)
		if !ok && value != nil {
			return nil, nil, fmt.Errorf("BASHPP-ECOLLECTION-SLICE: value is not sliceable")
		}
		low, high, max, err := r.bashPPSliceBounds(x, len(sequence), cap(sequence), meta.kind == "slice")
		if err != nil {
			return nil, nil, err
		}
		if max > cap(meta.sequence) {
			return nil, nil, fmt.Errorf("BASHPP-ECOLLECTION-STORAGE: missing slice capacity metadata")
		}
		var out []any
		var childSeq []*bashPPCollectionMeta
		if x.SecondColon.IsValid() {
			out = sequence[low:high:max]
			childSeq = meta.sequence[low:high:max]
		} else {
			out = sequence[low:high]
			childSeq = meta.sequence[low:high]
		}
		// Reslicing preserves the operand's own slice type: `s[i:j]` where s is a
		// named slice type Slice is itself a Slice, not the element-built []E.
		// Slicing an array (or array pointer) instead yields a fresh []E, since
		// the array's named type is not a slice type; rebuild it from the element.
		childType := meta.typ
		if meta.kind != "slice" || childType == nil {
			childType = &syntax.BashPPCollectionType{Kind: "slice", Element: collection.Element}
		}
		child := &bashPPCollectionMeta{kind: "slice", typ: childType, sequence: childSeq}
		if r.bashPPGoSource && sequence == nil {
			return nil, child, nil
		}
		return out, child, nil
	}
	return nil, nil, fmt.Errorf("BASHPP-ESELECTOR-EXPR: unsupported structured expression")
}

func (r *Runner) bashPPReadCellValue(cell *bashPPCell) (any, *bashPPCollectionMeta, error) {
	if cell == nil {
		return nil, nil, fmt.Errorf("Go value has no result")
	}
	// The carrier this picks and the payload it returns have to come from one
	// store, so a shared cell answers from a snapshot; see
	// bashpp_cell_share.go.
	cell = cell.view()
	if cell.pointer {
		return cell.pointerValue, bashPPPointerMeta(cell.declType), nil
	}
	if cell.interfaceValue != nil {
		return cell.vrValue(), &bashPPCollectionMeta{kind: "interface", typ: cell.declType, interfaceValue: cell.interfaceValue}, nil
	}
	if cell.vr.Kind == expand.Object {
		return cell.vr.Obj, bashPPCellMeta(cell), nil
	}
	if _, ok := r.bashPPUnderlyingType(cell.declType).(*syntax.BashPPFuncType); ok {
		if cell.vr.Str == "" || cell.vr.Str == "nil" {
			return nil, &bashPPCollectionMeta{kind: "func", typ: cell.declType}, nil
		}
		return cell.vr.Str, &bashPPCollectionMeta{kind: "func", typ: cell.declType}, nil
	}
	return bashPPScalarAny(r.bashPPScalarFromCell(cell).value), nil, nil
}

func (r *Runner) bashPPStructuredAssign(target, rhs syntax.BashPPExpr) {
	if r.bashPPNativeByteAssign(target, rhs) {
		return
	}
	if selector, ok := target.(*syntax.BashPPSelectorExpr); ok && r.bashPPNativeExpr(selector.X) {
		// Native handles bypass interpreter-side structured storage and would
		// otherwise dispatch this write directly to the dependency. Their
		// binding identity is still the authority for readonly, including an
		// alias which shares the same dependency-owned pointer.
		if root, rooted := bashPPCollectionRoot(target); rooted && r.bashPPScope != nil {
			if cell := r.bashPPScope.lookup(root); cell != nil && cell.object != nil && cell.object.readonly {
				if root != cell.object.owner {
					r.errf("BASHPP-EREADONLY-MUTATION: cannot mutate readonly value %q through alias %q and path %s\n", cell.object.owner, root, strings.TrimPrefix(bashPPExprText(target), root))
				} else {
					r.errf("BASHPP-EREADONLY-MUTATION: cannot mutate readonly value %q through field .%s\n", cell.object.owner, selector.Sel.Value)
				}
				r.exit = exitStatus{code: 2}
				return
			}
		}
		base, err := r.goSourceNativeAssignBase(selector.X)
		if err == nil {
			value, valueErr := r.bashPPBridgeExpr(rhs)
			if valueErr == nil {
				_, err = r.bashPPNativeAccess(r.ectx, "field-set", base, selector.Sel.Value, value)
			}
		}
		// An interrupted access (a goroutine unwound by program exit or a
		// panic) already carries its outcome.
		if err != nil && !errors.Is(err, errBashPPScalarInterrupted) {
			r.errf("%v\n", err)
			r.exit = exitStatus{code: 2}
		}
		return
	}
	if selector, ok := target.(*syntax.BashPPSelectorExpr); ok {
		if id, imported := selector.X.(*syntax.BashPPIdent); imported && r.bashPPImports[id.Name.Value] != "" {
			value, err := r.bashPPBridgeExpr(rhs)
			if err == nil {
				req, reqErr := r.bashPPEvalRequest()
				if reqErr != nil {
					err = reqErr
				} else {
					_, err = r.bashPPNativeRequest(r.ectx, req, bashPPBridgeRequest{Op: "var-set", Selector: id.Name.Value + "." + selector.Sel.Value, Args: []bashPPBridgeValue{value}})
				}
			}
			if err != nil {
				r.errf("%v\n", err)
				r.exit = exitStatus{code: 2}
			}
			return
		}
	}
	// Parentheses around an assignment target carry no meaning of their own,
	// so `(*p) = v` has to reach the same handler as `*p = v`. Without this,
	// a parenthesised dereference fell through to the pointer path below and
	// indexed an empty pointer path, crashing the host.
	for {
		paren, parenthesised := target.(*syntax.BashPPParenExpr)
		if !parenthesised {
			break
		}
		target = paren.X
	}
	if deref, ok := target.(*syntax.BashPPDerefExpr); ok {
		r.bashPPDerefAssign(deref, rhs)
		return
	}
	root, ok := bashPPCollectionRoot(target)
	// One snapshot: the pointer test here and the object identity the
	// readonly rules below consult describe the same store of the root, and
	// nothing writes back through this cell.
	cell := r.bashPPScope.lookup(root).view()
	if ok && cell != nil && cell.pointer || r.goSourceComputedPointerField(target) {
		if index, indexed := target.(*syntax.BashPPIndexExpr); indexed && r.bashPPGoSource {
			// A fault met at the target — a write into a nil map — is the
			// panic it always is; anything else is the diagnostic.
			if err := r.bashPPPointerElementAssign(index, rhs); err != nil {
				r.bashPPReportFault(err)
			}
			return
		}
		ptr, err := r.bashPPAddress(target)
		if err != nil {
			if !errors.Is(err, errBashPPScalarInterrupted) {
				r.errf("%v\n", err)
				r.exit = exitStatus{code: 2}
			}
			return
		}
		if ptr == nil {
			return
		}
		value, meta, err := r.bashPPEvalTypedValue(rhs, ptr.elem)
		if err != nil {
			if !errors.Is(err, errBashPPScalarInterrupted) {
				r.errf("BASHPP-EASSIGN-MISMATCH: %v\n", err)
				r.exit = exitStatus{code: 2}
			}
			return
		}
		if ptr.target.object != nil && ptr.target.object.readonly {
			r.errf("BASHPP-EREADONLY-MUTATION: cannot mutate readonly value %q through pointer\n", ptr.target.object.owner)
			r.exit = exitStatus{code: 2}
			return
		}
		if ptr.target.constant || ptr.target.vr.ReadOnly {
			r.errf("BASHPP-EREADONLY-MUTATION: cannot mutate readonly value through pointer\n")
			r.exit = exitStatus{code: 2}
			return
		}
		if ptr.unsafeOverlay() != nil {
			if err := ptr.unsafeOverlay().write(ptr, value, meta); err != nil {
				r.errf("%v\n", err)
				r.exit = exitStatus{code: 2}
			}
			return
		}
		parent, parentMeta, _, err := ptr.readParent()
		if err != nil {
			// `lst.head.next = v` with a nil head is the nil dereference.
			r.bashPPReportFault(err)
			return
		}
		// Go inserts an implicit dereference between selector components. The
		// address path normally records that step, but instantiated generic
		// receiver fields can retain the pointer as the parent value instead.
		// Follow that storage identity before writing the final field, just as
		// an explicit (*parent).field assignment would.
		for {
			parentPointer, pointerParent := parent.(*bashPPPointer)
			if !pointerParent {
				break
			}
			if parentPointer == nil {
				r.bashPPReportNilDereference()
				return
			}
			parent, parentMeta, _, err = parentPointer.read()
			if err != nil {
				r.errf("%v\n", err)
				r.exit = exitStatus{code: 2}
				return
			}
		}
		if parentMeta == nil {
			r.errf("BASHPP-ESELECTOR-TYPE: assignment parent is not a structured value\n")
			r.exit = exitStatus{code: 2}
			return
		}
		if len(ptr.path) == 0 {
			// The pointer names a whole value rather than a place inside one,
			// so there is no parent to write into. Every such target is
			// handled above; reaching here means a shape this path cannot
			// address, which is reported rather than left to index nothing.
			r.errf("BASHPP-ESELECTOR-ASSIGN: target is not a structured value\n")
			r.exit = exitStatus{code: 2}
			return
		}
		last := ptr.path[len(ptr.path)-1]
		if last.field != "" {
			mapping, ok := parent.(map[string]any)
			if !ok {
				r.errf("BASHPP-ESELECTOR-TYPE: assignment parent is not struct storage\n")
				r.exit = exitStatus{code: 2}
				return
			}
			bashPPStorageSetField(mapping, parentMeta.mapping, last.field, value, meta)
		} else {
			sequence, ok := parent.([]any)
			if !ok || last.index < 0 || last.index >= len(sequence) {
				r.errf("BASHPP-ESELECTOR-TYPE: assignment parent is not collection storage\n")
				r.exit = exitStatus{code: 2}
				return
			}
			sequence[last.index] = value
			parentMeta.sequence[last.index] = meta
		}
		return
	}
	if !ok || cell == nil || cell.object == nil {
		r.errf("BASHPP-ESELECTOR-ASSIGN: target is not a structured value\n")
		r.exit = exitStatus{code: 2}
		return
	}
	path := strings.TrimPrefix(bashPPExprText(target), root)
	if cell.object.readonly {
		if root != cell.object.owner {
			r.errf("BASHPP-EREADONLY-MUTATION: cannot mutate readonly value %q through alias %q and path %s\n", cell.object.owner, root, path)
		} else {
			kind := "field"
			parentExpr := bashPPParentExpr(target)
			_, parentMeta, _ := r.bashPPReadExpr(parentExpr)
			if parentMeta != nil && parentMeta.kind != "struct" {
				kind = "slice"
				if parentMeta.kind == "map" {
					kind = "map"
				}
			}
			if kind == "field" {
				detail := path
				if dot := strings.LastIndex(path, "."); dot >= 0 {
					detail = path[dot:]
				}
				r.errf("BASHPP-EREADONLY-MUTATION: cannot mutate readonly value %q through field %s\n", cell.object.owner, detail)
			} else {
				r.errf("BASHPP-EREADONLY-MUTATION: cannot mutate readonly value %q through %s path %s\n", cell.object.owner, kind, path)
			}
		}
		r.exit = exitStatus{code: 2}
		return
	}
	if bashPPCellMeta(cell) == nil {
		r.errf("BASHPP-ESELECTOR-ASSIGN: target is not a supported typed structured value\n")
		r.exit = exitStatus{code: 2}
		return
	}
	parentExpr := bashPPParentExpr(target)
	parent, parentMeta, err := r.bashPPReadExpr(parentExpr)
	if err != nil {
		if !errors.Is(err, errBashPPScalarInterrupted) {
			r.errf("%v\n", err)
			r.exit = exitStatus{code: 2}
		}
		return
	}
	// When the parent expression itself names a pointer -- e.g. a struct field
	// of pointer type, as in `lst.tail.next = v` where `tail` is `*element` --
	// the write lands in the struct the pointer refers to, not in the pointer
	// value. Follow it to that storage before resolving the field, mirroring the
	// deref bashPPReadExpr already performs when reading such selectors. The map
	// returned aliases the pointed-at struct, so the write mutates it in place.
	// A non-pointer parent is returned unchanged; a nil pointer is a nil deref.
	if parent, parentMeta, err = bashPPDerefEmbedded(parent, parentMeta); err != nil {
		r.bashPPReportFault(err)
		return
	}
	if parentMeta == nil {
		r.errf("BASHPP-ESELECTOR-TYPE: assignment parent is not a structured value\n")
		r.exit = exitStatus{code: 2}
		return
	}
	var expected syntax.BashPPTypeExpr
	switch x := target.(type) {
	case *syntax.BashPPSelectorExpr:
		sel := r.bashPPResolveField(parentMeta.typ, x.Sel.Value)
		if sel.ambiguous || len(sel.edges) == 0 {
			err = bashPPSelectionError(parentMeta.typ, x.Sel.Value, sel)
			break
		}
		expected = sel.fieldType
		value, child, valueErr := r.bashPPEvalTypedValue(rhs, expected)
		if valueErr != nil {
			err = valueErr
			break
		}
		if len(sel.edges) > 1 {
			parent, parentMeta, err = bashPPReadSelection(parent, parentMeta, sel.edges[:len(sel.edges)-1])
			if err != nil {
				break
			}
			parent, parentMeta, err = bashPPDerefEmbedded(parent, parentMeta)
			if err != nil {
				break
			}
		}
		last := sel.edges[len(sel.edges)-1].name
		mapping, ok := parent.(map[string]any)
		if !ok || parentMeta == nil || parentMeta.kind != "struct" {
			err = fmt.Errorf("BASHPP-ESELECTOR-TYPE: assignment parent is not struct storage")
			break
		}
		bashPPStorageSetField(mapping, parentMeta.mapping, last, value, child)
	case *syntax.BashPPIndexExpr:
		collection, found := r.bashPPUnderlyingType(parentMeta.typ).(*syntax.BashPPCollectionType)
		if !found {
			err = fmt.Errorf("BASHPP-ECOLLECTION-ASSIGN: indexed target is not a collection")
			break
		}
		expected = collection.Element
		var savedKey any
		var savedKeyMeta *bashPPCollectionMeta
		var savedIndex bashPPCollectionIndexValue
		if r.bashPPGoSource {
			if parentMeta.kind == "map" {
				savedKey, savedKeyMeta, err = r.bashPPEvalElement(x.Index, collection.Key)
			} else {
				savedIndex, err = r.bashPPCollectionIndex(x.Index)
			}
			if err != nil {
				break
			}
		}
		value, child, valueErr := r.bashPPEvalTypedValue(rhs, expected)
		if valueErr != nil {
			err = valueErr
			break
		}
		if parentMeta.kind == "map" {
			err = r.bashPPMapElementWrite(parent, parentMeta, collection, x.Index, savedKey, savedKeyMeta, value, child)
			break
		} else {
			index, indexErr := savedIndex, error(nil)
			if !r.bashPPGoSource {
				index, indexErr = r.bashPPCollectionIndex(x.Index)
			}
			if indexErr != nil {
				err = indexErr
				break
			}
			sequence, valid := parent.([]any)
			if !valid && parent != nil {
				err = fmt.Errorf("BASHPP-ECOLLECTION-STORAGE: invalid sequence payload")
				break
			}
			if index.outOfBounds(len(sequence)) {
				if r.bashPPGoSource {
					err = r.bashPPSprint162CollectionBoundsPanic(x, index, len(sequence))
				} else {
					err = fmt.Errorf("BASHPP-ECOLLECTION-BOUNDS: index %s out of bounds for length %d", index.text, len(sequence))
				}
				break
			}
			i := index.value
			if i >= len(parentMeta.sequence) {
				err = fmt.Errorf("BASHPP-ECOLLECTION-STORAGE: missing element metadata")
				break
			}
			sequence[i], parentMeta.sequence[i] = value, child
			if child != nil && (bashPPValueMeta(child) || child.interfaceValue != nil) {
				parentMeta.hasValueElements = true
				parentMeta.hasValueElementsKnown = true
			}
		}
	}
	if err != nil {
		// A fault met on the way to the target — a nil pointer in the path —
		// is the panic it always is; anything else is the diagnostic.
		r.bashPPReportFault(err)
	}
}

// bashPPMapElementWrite stores one element into a map's shared storage. Go
// does not make map elements addressable, so this write -- not a write through
// an address -- is the only way any indexed map target is assigned, whether
// the map is named directly or reached through a pointer.
func (r *Runner) bashPPMapElementWrite(parent any, parentMeta *bashPPCollectionMeta, collection *syntax.BashPPCollectionType, index syntax.BashPPExpr, savedKey any, savedKeyMeta *bashPPCollectionMeta, value any, child *bashPPCollectionMeta) error {
	if parent == nil {
		return errBashPPNilMapAssign
	}
	key := savedKey
	keyMeta := savedKeyMeta
	if !r.bashPPGoSource {
		var keyErr error
		if key, keyMeta, keyErr = r.bashPPEvalElement(index, collection.Key); keyErr != nil {
			return keyErr
		}
	}
	mapping, valid := parent.(map[string]any)
	if !valid || mapping == nil {
		return errBashPPNilMapAssign
	}
	_, err := r.bashPPSprint165MapStore(mapping, parentMeta, key, keyMeta, collection.Key, value, child)
	return err
}

// bashPPPointerElementAssign retains the evaluated parent storage for GoSource
// indexed writes. Maps are not addressable; slices and arrays must not fall
// back to another expression walk after evaluating a possibly effectful parent.
func (r *Runner) bashPPPointerElementAssign(target *syntax.BashPPIndexExpr, rhs syntax.BashPPExpr) error {
	parent, parentMeta, err := r.bashPPReadExpr(target.X)
	if err != nil {
		return err
	}
	// `p[i] = v` on a pointer to an array is `(*p)[i] = v`: Go inserts the
	// dereference, and a nil p is the nil dereference it always is (a Go
	// runtime panic). Any other pointer-held collection storage is read the
	// same way afterwards.
	if parent, parentMeta, err = r.goSourceIndexedPointee(parent, parentMeta); err != nil {
		return err
	}
	parent, parentMeta, err = r.bashPPSprint162PointerCollectionStorage(parent, parentMeta)
	if err != nil {
		return err
	}
	if parentMeta == nil {
		return fmt.Errorf("BASHPP-ECOLLECTION-ASSIGN: indexed target is not a collection")
	}
	collection, found := r.bashPPUnderlyingType(parentMeta.typ).(*syntax.BashPPCollectionType)
	if !found {
		return fmt.Errorf("BASHPP-ECOLLECTION-ASSIGN: indexed target is not a collection")
	}
	// The map itself is addressable even though its elements are not, so the
	// readonly guards that protect any other write through a pointer still
	// apply. They are read off the root cell rather than by re-walking the
	// operand, so that an index expression along the way -- `(*p)[next()][k]`
	// -- is not evaluated a second time just to answer a readonly question.
	if root, named := bashPPCollectionRoot(target.X); named && r.bashPPScope != nil {
		owner := r.bashPPScope.lookup(root).view()
		if owner != nil && owner.pointer && owner.pointerValue != nil {
			// The pointee is storage a task can alias too, so the hop takes its
			// own snapshot rather than reading the cell the pointer names.
			owner = owner.pointerValue.target.view()
		}
		if owner != nil {
			if owner.object != nil && owner.object.readonly {
				return fmt.Errorf("BASHPP-EREADONLY-MUTATION: cannot mutate readonly value %q through pointer", owner.object.owner)
			}
			if owner.constant || owner.vr.ReadOnly {
				return fmt.Errorf("BASHPP-EREADONLY-MUTATION: cannot mutate readonly value through pointer")
			}
		}
	}
	var savedKey any
	var savedKeyMeta *bashPPCollectionMeta
	var savedIndex bashPPCollectionIndexValue
	// Resolve all operands once, before the RHS, retaining the parent storage.
	if parentMeta.kind == "map" {
		savedKey, savedKeyMeta, err = r.bashPPEvalElement(target.Index, collection.Key)
	} else {
		savedIndex, err = r.bashPPCollectionIndex(target.Index)
	}
	if err != nil {
		return err
	}
	value, child, err := r.bashPPEvalTypedValue(rhs, collection.Element)
	if err != nil {
		return err
	}
	if parentMeta.kind == "map" {
		return r.bashPPMapElementWrite(parent, parentMeta, collection, target.Index, savedKey, savedKeyMeta, value, child)
	}
	sequence, valid := parent.([]any)
	if !valid && parent != nil {
		return fmt.Errorf("BASHPP-ECOLLECTION-STORAGE: invalid sequence payload")
	}
	if savedIndex.outOfBounds(len(sequence)) {
		if r.bashPPGoSource {
			return r.bashPPSprint162CollectionBoundsPanic(target, savedIndex, len(sequence))
		}
		return fmt.Errorf("BASHPP-ECOLLECTION-BOUNDS: index %s out of bounds for length %d", savedIndex.text, len(sequence))
	}
	i := savedIndex.value
	if i >= len(parentMeta.sequence) {
		return fmt.Errorf("BASHPP-ECOLLECTION-STORAGE: missing element metadata")
	}
	sequence[i], parentMeta.sequence[i] = value, child
	if child != nil && (bashPPValueMeta(child) || child.interfaceValue != nil) {
		parentMeta.hasValueElements = true
		parentMeta.hasValueElementsKnown = true
	}
	return nil
}

func bashPPParentExpr(expr syntax.BashPPExpr) syntax.BashPPExpr {
	switch x := expr.(type) {
	case *syntax.BashPPSelectorExpr:
		return x.X
	case *syntax.BashPPIndexExpr:
		return x.X
	}
	return expr
}

// bashPPCompositeAddress materializes `&T{…}` — a composite literal taken by
// address. Go allows it although the literal is not a variable: the result is
// a pointer to a fresh value with its own lifetime, so the literal is stored
// in an anonymous cell which the returned pointer owns.
func (r *Runner) bashPPCompositeAddress(lit *syntax.BashPPCompositeLit) (*bashPPPointer, error) {
	typ := lit.LitType
	if typ == nil {
		return nil, fmt.Errorf("BASHPP-ENONADDRESSABLE: composite literal has no type")
	}
	value, meta, err := r.bashPPEvalComposite(lit, nil)
	if err != nil {
		return nil, err
	}
	// [...]T is not the type of the resulting value; it is syntax asking Go
	// to infer a concrete array length. Preserve that concrete identity when
	// the literal is addressed, too, so &[...]T{...} has type *[N]T.
	if collection, ok := r.bashPPUnderlyingType(typ).(*syntax.BashPPCollectionType); ok && collection.Kind == "inferred-array" {
		sequence, _ := value.([]any)
		typ = &syntax.BashPPCollectionType{
			Kind:    "array",
			Length:  &syntax.Lit{Value: strconv.Itoa(len(sequence))},
			Element: collection.Element,
		}
		if meta != nil {
			meta.kind, meta.typ = "array", typ
		}
	}
	cell := &bashPPCell{declType: typ}
	if named, ok := typ.(*syntax.BashPPNamedType); ok && named.Name != nil {
		cell.typeName = named.Name.Value
	}
	bashPPStoreCellValue(cell, value, meta)
	return r.bashPPPointerForStorage(cell, typ, true), nil
}

// bashPPRepresentableScalar converts an untyped constant toward the type it is
// being stored in, the way Go does. `p.X = 1e9` assigns an int field because
// 1e9 denotes an exact integer, even though the constant is written in
// floating form; a constant that is not exactly an integer is left alone so
// the assignment is still refused.
func (r *Runner) bashPPRepresentableScalar(scalar bashPPScalar, expected syntax.BashPPTypeExpr) bashPPScalar {
	if scalar.typ != "" || scalar.value == nil || scalar.value.Kind() != constant.Float || expected == nil {
		return scalar
	}
	name, ok := r.bashPPUnderlyingType(expected).(*syntax.BashPPNamedType)
	if !ok || !bashPPIntegerType(name.Name.Value) {
		return scalar
	}
	if integer := constant.ToInt(scalar.value); integer.Kind() == constant.Int {
		scalar.value = integer
	}
	return scalar
}

// goSourceComputedPointerField reports a field assignment whose operand is a
// computed pointer rather than a variable: `t.extra.(*Tuple).first = v`. Go
// makes the field addressable through the implicit dereference, so the write
// goes through the address of the selected field exactly as it does for a
// pointer variable.
func (r *Runner) goSourceComputedPointerField(target syntax.BashPPExpr) bool {
	selector, ok := target.(*syntax.BashPPSelectorExpr)
	if !ok || !r.bashPPGoSource {
		return false
	}
	x := selector.X
	for {
		paren, parenthesised := x.(*syntax.BashPPParenExpr)
		if !parenthesised {
			break
		}
		x = paren.X
	}
	if call, ok := x.(*syntax.BashPPCall); ok {
		// f().field and s.StructType().ParamTuple: a call result of pointer
		// type is dereferenced implicitly, so its field is addressable.
		return r.goSourcePointerCallResult(call)
	}
	if conv, ok := x.(*syntax.BashPPConvertExpr); ok {
		_, pointer := r.bashPPPointerType(r.bashPPConvertTarget(conv))
		return pointer
	}
	assert, ok := x.(*syntax.BashPPTypeAssertExpr)
	if !ok {
		return false
	}
	_, pointer := r.bashPPPointerType(assert.Assert)
	return pointer
}

// goSourcePointerCallResult reports a call whose one checked result is a
// pointer. Only the front end's checked signature is consulted; the call is
// not evaluated.
func (r *Runner) goSourcePointerCallResult(call *syntax.BashPPCall) bool {
	typ, known := syntax.BashPPTypeExpr(nil), false
	if len(call.ResultTypes) == 1 {
		typ, known = call.ResultTypes[0], true
	} else {
		typ, known = r.goSourceStaticExprType(call)
	}
	if !known || typ == nil {
		return false
	}
	_, pointer := r.bashPPPointerType(typ)
	return pointer
}
