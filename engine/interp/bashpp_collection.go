// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"fmt"
	goast "go/ast"
	"go/constant"
	goparser "go/parser"
	gotoken "go/token"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// bashPPCollectionMeta keeps the Go collection identity which a JSON-shaped
// object payload cannot express. Nested metadata follows the payload exactly.
type bashPPCollectionMeta struct {
	kind     string
	typ      syntax.BashPPTypeExpr
	sequence []*bashPPCollectionMeta
	// sequenceLen/sequenceCap let a slice carry its Go length and capacity
	// independently from the dense interpreter carrier. Zero values mean the
	// ordinary []any representation is authoritative.
	sequenceLen int
	sequenceCap int
	mapping     map[string]*bashPPCollectionMeta
	mapKeys     map[bashPPMapKey]*bashPPMapEntry
	mapNonce    uint64
	// mapOrder caches the sorted iteration order of mapKeys so that starting
	// a range does not re-sort the whole map (range-then-break over a map
	// being drained would otherwise be quadratic). It is rebuilt after any
	// insertion of a new key; deletions only mark entries dead, counted in
	// mapOrderDead, and the cache is compacted once dead entries dominate.
	// Slices handed out are never mutated in place.
	mapOrder      []*bashPPMapEntry
	mapOrderDead  int
	mapOrderValid bool
	// interfaceValue preserves the dynamic type and value of an interface
	// stored inside a collection or struct. The JSON-shaped payload alone can
	// only retain its printable shell value.
	interfaceValue *bashPPInterfaceValue
	// channel preserves the identity of an interpreter-owned channel stored in
	// a struct field, slice element or map value. A channel is a reference, so
	// what a payload has to carry across a copy is which channel it names —
	// something no rendering of the value can express. A dependency-owned
	// channel needs no entry here: its handle IS the payload.
	channel      *bashPPChannel
	channelOwner *bashPPConcurrent
	// hasValueElements reports whether any element of this collection or array
	// has value semantics (structs, arrays, interfaces) requiring recursive deep-copy.
	hasValueElements      bool
	hasValueElementsKnown bool
}

func bashPPArrayMeta(meta *bashPPCollectionMeta) bool {
	return meta != nil && (meta.kind == "array" || meta.kind == "inferred-array")
}

func bashPPSequenceLen(meta *bashPPCollectionMeta, dense []any) int {
	if meta != nil && meta.sequenceLen != 0 {
		return meta.sequenceLen
	}
	return len(dense)
}

func bashPPSequenceCap(meta *bashPPCollectionMeta, dense []any) int {
	if meta != nil && meta.sequenceCap != 0 {
		return meta.sequenceCap
	}
	return cap(dense)
}

func bashPPLogicalSequence(meta *bashPPCollectionMeta) bool {
	return meta != nil && (meta.sequenceLen != 0 || meta.sequenceCap != 0)
}

func bashPPSetSequenceShape(meta *bashPPCollectionMeta, length, capacity int) {
	if meta == nil {
		return
	}
	if length == len(meta.sequence) && capacity == cap(meta.sequence) {
		meta.sequenceLen, meta.sequenceCap = 0, 0
		return
	}
	meta.sequenceLen, meta.sequenceCap = length, capacity
}

func bashPPValueMeta(meta *bashPPCollectionMeta) bool {
	return bashPPArrayMeta(meta) || meta != nil && meta.kind == "struct"
}

// bashPPElemHasValueSemantics reports whether array element type expr has value semantics.
func bashPPElemHasValueSemantics(elem syntax.BashPPTypeExpr) bool {
	if elem == nil {
		return true
	}
	switch x := elem.(type) {
	case *syntax.BashPPNamedType:
		if x.Name == nil {
			return true
		}
		switch x.Name.Value {
		case "int", "int8", "int16", "int32", "int64",
			"uint", "uint8", "uint16", "uint32", "uint64", "uintptr",
			"float32", "float64", "complex64", "complex128",
			"bool", "string", "byte", "rune":
			return false
		default:
			return true
		}
	case *syntax.BashPPPointerType, *syntax.BashPPChanType, *syntax.BashPPFuncType:
		return false
	case *syntax.BashPPCollectionType:
		if x.Kind == "slice" || x.Kind == "map" {
			return false
		}
		return true
	case *syntax.BashPPStructType, *syntax.BashPPInterfaceType:
		return true
	}
	return true
}

func (r *Runner) bashPPTypeHasValueElements(typ syntax.BashPPTypeExpr) bool {
	if typ == nil {
		return true
	}
	underlying := r.bashPPUnderlyingType(typ)
	return bashPPElemHasValueSemantics(underlying)
}

// arrayHasValueElements reports whether the array needs recursive deep-copy.
// It is a pure read-only query: concurrent by-value copies of the same source
// array are legal Go and must not race, so this path never materializes the
// lazy cache on the shared source meta. Write paths precompute the flags at
// creation and assignment; copies carry the computed result forward on their
// private metadata. The scalar fast path below keeps bulk copies O(1) without
// scanning element metadata.
func (meta *bashPPCollectionMeta) arrayHasValueElements() bool {
	if meta == nil {
		return false
	}
	if meta.hasValueElementsKnown {
		return meta.hasValueElements
	}
	if col, ok := meta.typ.(*syntax.BashPPCollectionType); ok && col.Kind == "array" && col.Element != nil {
		if !bashPPElemHasValueSemantics(col.Element) {
			return false
		}
	}
	for _, child := range meta.sequence {
		if bashPPValueMeta(child) || (child != nil && child.interfaceValue != nil) {
			return true
		}
	}
	return false
}

// bashPPCopyArrayValue applies Go's value semantics to arrays without
// accidentally deep-copying slice or map elements, which remain reference
// values. Nested array elements are copied recursively.
func bashPPCopyArrayValue(value any, meta *bashPPCollectionMeta) (any, *bashPPCollectionMeta) {
	if meta == nil {
		return value, meta
	}
	metaCopy := *meta
	if meta.interfaceValue != nil {
		iface := *meta.interfaceValue
		if meta.interfaceValue.cell != nil {
			iface.cell = bashPPCopyInterfaceCell(meta.interfaceValue.cell)
		}
		metaCopy.interfaceValue = &iface
		if iface.nilIface || iface.cell == nil {
			return "", &metaCopy
		}
		return iface.cell.vrValue(), &metaCopy
	}
	if !bashPPValueMeta(meta) {
		return value, meta
	}
	if meta.kind == "struct" {
		mapping, ok := value.(map[string]any)
		if !ok {
			return value, meta
		}
		mapping = bashPPStorageSnapshot(mapping)
		layout := bashPPLayoutSnapshot(meta.mapping)
		out := make(map[string]any, len(mapping))
		metaCopy.mapping = nil
		for field, item := range mapping {
			child := layout[field]
			if bashPPValueMeta(child) || child != nil && child.interfaceValue != nil {
				item, child = bashPPCopyArrayValue(item, child)
			}
			bashPPSetInitialStructField(out, &metaCopy, field, item, child)
		}
		return out, &metaCopy
	}
	sequence, ok := value.([]any)
	if !ok {
		return value, meta
	}
	// An array's capacity is its length, so the copy is allocated at exactly
	// that size for both the payload and its metadata; append's size-class
	// rounding would give the two different spare capacities, which cap()
	// and a re-slice up to cap would then observe.
	out := make([]any, len(sequence))
	copy(out, sequence)
	if len(meta.sequence) > 0 {
		metaCopy.sequence = make([]*bashPPCollectionMeta, len(meta.sequence))
		copy(metaCopy.sequence, meta.sequence)
	}
	if !meta.arrayHasValueElements() {
		metaCopy.hasValueElements = false
		metaCopy.hasValueElementsKnown = true
		return out, &metaCopy
	}
	for i, child := range metaCopy.sequence {
		if bashPPValueMeta(child) || child != nil && child.interfaceValue != nil {
			out[i], metaCopy.sequence[i] = bashPPCopyArrayValue(out[i], child)
		}
	}
	metaCopy.hasValueElements = true
	metaCopy.hasValueElementsKnown = true
	return out, &metaCopy
}

func bashPPCloneCollectionMeta(meta *bashPPCollectionMeta, seen map[*bashPPCollectionMeta]*bashPPCollectionMeta, cloneCell func(*bashPPCell) *bashPPCell) *bashPPCollectionMeta {
	if meta == nil {
		return nil
	}
	if done := seen[meta]; done != nil {
		return done
	}
	bashPPStorageMu.RLock()
	out := *meta
	bashPPStorageMu.RUnlock()
	out.mapOrder, out.mapOrderDead, out.mapOrderValid = nil, 0, false
	seen[meta] = &out
	if meta.interfaceValue != nil {
		iface := *meta.interfaceValue
		if meta.interfaceValue.cell != nil {
			if cloneCell != nil {
				iface.cell = cloneCell(meta.interfaceValue.cell)
			} else {
				iface.cell = bashPPCopyInterfaceCell(meta.interfaceValue.cell)
			}
		}
		out.interfaceValue = &iface
	}
	// A slice header's metadata is a parallel view of the same backing
	// capacity as its payload. Preserve that capacity across snapshots so a
	// later legal re-slice does not outrun its element metadata.
	out.sequence = make([]*bashPPCollectionMeta, len(meta.sequence), cap(meta.sequence))
	out.sequenceLen, out.sequenceCap = meta.sequenceLen, meta.sequenceCap
	for i, child := range meta.sequence[:cap(meta.sequence)] {
		out.sequence[:cap(out.sequence)][i] = bashPPCloneCollectionMeta(child, seen, cloneCell)
	}
	if meta.mapping != nil {
		layout := bashPPLayoutSnapshot(meta.mapping)
		out.mapping = make(map[string]*bashPPCollectionMeta, len(layout))
		for key, child := range layout {
			out.mapping[key] = bashPPCloneCollectionMeta(child, seen, cloneCell)
		}
	}
	if entries := bashPPSprint165MapEntryTable(meta); entries != nil {
		out.mapKeys = make(map[bashPPMapKey]*bashPPMapEntry, len(entries))
		for key, entry := range entries {
			entryCopy := *entry
			entryCopy.keyMeta = bashPPCloneCollectionMeta(entry.keyMeta, seen, cloneCell)
			entryCopy.key, entryCopy.keyMeta = bashPPCopyArrayValue(entry.key, entryCopy.keyMeta)
			if pointer, ok := entry.key.(*bashPPPointer); ok && cloneCell != nil && pointer != nil {
				pointerCopy := *pointer
				pointerCopy.target = cloneCell(pointer.target)
				pointerCopy.path = append([]bashPPPointerStep(nil), pointer.path...)
				entryCopy.key = &pointerCopy
				key.value = bashPPSprint165PointerMapValue(&pointerCopy)
			}
			out.mapKeys[key] = &entryCopy
		}
	}
	return &out
}

func bashPPTypeText(typ syntax.BashPPTypeExpr) string {
	switch x := typ.(type) {
	case *syntax.BashPPChanType:
		prefix := "chan "
		if x.Direction == "send" {
			prefix = "chan<- "
		}
		if x.Direction == "recv" {
			prefix = "<-chan "
		}
		if x.Element != nil {
			return prefix + bashPPTypeText(x.Element)
		}
		if x.Elem != nil {
			return prefix + x.Elem.Value
		}
		return prefix
	case *syntax.BashPPNamedType:
		if len(x.TypeArgs) == 0 {
			return x.Name.Value
		}
		var args []string
		for _, arg := range x.TypeArgs {
			args = append(args, bashPPTypeText(arg.ArgType))
		}
		return x.Name.Value + "[" + strings.Join(args, ", ") + "]"
	case *syntax.BashPPTypeParamType:
		return x.Name.Value
	case *syntax.BashPPUnionType:
		var terms []string
		for _, term := range x.Terms {
			terms = append(terms, bashPPTypeText(term))
		}
		return strings.Join(terms, " | ")
	case *syntax.BashPPApproxType:
		return "~" + bashPPTypeText(x.Term)
	case *syntax.BashPPCollectionType:
		if x.Kind == "map" {
			return "map[" + bashPPTypeText(x.Key) + "]" + bashPPTypeText(x.Element)
		}
		length := ""
		if x.Length != nil {
			length = x.Length.Value
		}
		return "[" + length + "]" + bashPPTypeText(x.Element)
	case *syntax.BashPPStructType:
		return "struct"
	case *syntax.BashPPPointerType:
		return "*" + bashPPTypeText(x.Element)
	case *syntax.BashPPFuncType:
		return "func(" + bashPPFieldsSignature(x.Params) + ")(" + bashPPFieldsSignature(x.Results) + ")"
	case *syntax.BashPPInterfaceType:
		return "interface"
	}
	return "<inferred>"
}

func (r *Runner) bashPPUnderlyingType(typ syntax.BashPPTypeExpr) syntax.BashPPTypeExpr {
	// The cycle guard compares "name@scope" keys of the declarations already
	// expanded on this walk. A key is only needed once a second declaration
	// is reached, so the common one-step resolution (an alias or defined
	// type straight to its literal, or a predeclared type) spells no key
	// and allocates nothing. Keys are pure functions of the reference and
	// the unchanged registry, so computing them late compares the same.
	var chainStore [4]*syntax.BashPPNamedType
	chain := chainStore[:0]
	var keys []string
	for {
		name, ok := typ.(*syntax.BashPPNamedType)
		if !ok {
			return typ
		}
		decl, found := r.bashPPTypeDeclarationForReference(name)
		if !found || decl.typeExpr == nil {
			return typ
		}
		if len(chain) > 0 {
			for len(keys) < len(chain) {
				keys = append(keys, r.bashPPUnderlyingTypeKey(chain[len(keys)]))
			}
			if slices.Contains(keys, r.bashPPUnderlyingTypeKey(name)) {
				return typ
			}
		}
		chain = append(chain, name)
		typ = bashPPInstantiateTypeDeclaration(name, decl)
	}
}

// bashPPUnderlyingTypeName resolves the named-type chain for a type spelled
// `name` and returns the underlying type's name when the underlying type is
// itself a named type — the same result as asserting bashPPUnderlyingType's
// return to *syntax.BashPPNamedType and reading Name.Value. It differs only in
// cost: when `name` has no user type declaration (a predeclared type such as
// int, float64 or string) the underlying type is the name itself, so it
// returns immediately without allocating the wrapper AST nodes
// bashPPUnderlyingType requires. The scalar arithmetic hot path resolves the
// underlying type of every operand and result, so that predeclared case
// dominates; see the Sprint 270 G2 measurements. Ok is false only when the
// underlying type is not a plain named type (e.g. a slice or map).
func (r *Runner) bashPPUnderlyingTypeName(name string) (string, bool) {
	if name == "" {
		return "", false
	}
	if _, declared := r.bashPPTypes[name]; !declared {
		// bashPPTypeDeclarationForReference finds nothing, so
		// bashPPUnderlyingType returns the reference unchanged: the underlying
		// name is `name` itself.
		return name, true
	}
	named, ok := r.bashPPUnderlyingType(&syntax.BashPPNamedType{Name: &syntax.Lit{Value: name}}).(*syntax.BashPPNamedType)
	if !ok || named.Name == nil {
		return "", false
	}
	return named.Name.Value, true
}

// bashPPUnderlyingTypeKey is the cycle-guard identity of a named reference:
// its spelling plus the lexical scope of the local declaration it names.
func (r *Runner) bashPPUnderlyingTypeKey(name *syntax.BashPPNamedType) string {
	key := bashPPTypeText(name)
	if scope, known := r.goSourceLocalTypeScope(name); known {
		key += "@" + scope
	}
	return key
}

func (r *Runner) bashPPTypeAssignable(actual, expected syntax.BashPPTypeExpr) bool {
	if expected == nil {
		return true
	}
	actual = r.bashPPPredeclaredAliases(r.bashPPCanonicalAssignableType(actual))
	expected = r.bashPPPredeclaredAliases(r.bashPPCanonicalAssignableType(expected))
	if bashPPTypeText(actual) == bashPPTypeText(expected) {
		return true
	}
	if r.goSourceImportedScalarTypesIdentical(bashPPTypeText(actual), bashPPTypeText(expected)) {
		return true
	}
	actual, expected = r.bashPPResolvedArrayLengths(actual), r.bashPPResolvedArrayLengths(expected)
	if bashPPTypeText(actual) == bashPPTypeText(expected) {
		return true
	}
	if r.goSourceImportTypesIdentical(actual, expected) {
		return true
	}
	_, actualNamed := actual.(*syntax.BashPPNamedType)
	_, expectedNamed := expected.(*syntax.BashPPNamedType)
	if actualNamed && expectedNamed {
		return false
	}
	actualUnder := r.bashPPResolvedArrayLengths(r.bashPPUnderlyingType(actual))
	expectedUnder := r.bashPPResolvedArrayLengths(r.bashPPUnderlyingType(expected))
	return bashPPTypeText(actualUnder) == bashPPTypeText(expectedUnder) ||
		r.goSourceImportTypesIdentical(actualUnder, expectedUnder)
}

// bashPPInferredArrayAssignable reports whether an inferred-length array
// value — a [...]T literal — may serve a fixed-length array destination. The
// spelling [...]T carries no length, so the type texts can never agree; the
// value's own element count is the length Go inferred, and it must equal the
// destination's resolved length with the element types spelling the same.
func (r *Runner) bashPPInferredArrayAssignable(meta *bashPPCollectionMeta, expected syntax.BashPPTypeExpr) bool {
	if meta == nil || meta.kind != "inferred-array" {
		return false
	}
	actual, ok := r.bashPPUnderlyingType(meta.typ).(*syntax.BashPPCollectionType)
	if !ok || actual.Kind != "inferred-array" {
		return false
	}
	array, ok := r.bashPPUnderlyingType(expected).(*syntax.BashPPCollectionType)
	if !ok || array.Kind != "array" || array.Length == nil {
		return false
	}
	n, err := r.bashPPArrayLength(array.Length.Value)
	if err != nil || n != len(meta.sequence) {
		return false
	}
	actualElement, expectedElement := r.bashPPResolvedArrayLengths(actual.Element), r.bashPPResolvedArrayLengths(array.Element)
	return bashPPTypeText(actualElement) == bashPPTypeText(expectedElement) ||
		r.goSourceImportTypesIdentical(actualElement, expectedElement)
}

// bashPPResolvedArrayLengths spells every array length in typ as its resolved
// integer constant, so a length written as a constant expression compares
// equal to its value — `[len(at)]*T` is the type `[3]*T` when at is a
// three-element array. A length that does not resolve to an integer constant
// in the current scope is left as written.
func (r *Runner) bashPPResolvedArrayLengths(typ syntax.BashPPTypeExpr) syntax.BashPPTypeExpr {
	switch x := typ.(type) {
	case *syntax.BashPPCollectionType:
		key, element := x.Key, r.bashPPResolvedArrayLengths(x.Element)
		if x.Kind == "map" && key != nil {
			key = r.bashPPResolvedArrayLengths(key)
		}
		length := x.Length
		if x.Kind == "array" && length != nil {
			if _, err := strconv.Atoi(length.Value); err != nil {
				if n, err := r.bashPPArrayLength(length.Value); err == nil {
					length = &syntax.Lit{ValuePos: x.Length.ValuePos, ValueEnd: x.Length.ValueEnd, Value: strconv.Itoa(n)}
				}
			}
		}
		if key == x.Key && element == x.Element && length == x.Length {
			return x
		}
		resolved := *x
		resolved.Key, resolved.Element, resolved.Length = key, element, length
		return &resolved
	case *syntax.BashPPPointerType:
		element := r.bashPPResolvedArrayLengths(x.Element)
		if element == x.Element {
			return x
		}
		resolved := *x
		resolved.Element = element
		return &resolved
	case *syntax.BashPPChanType:
		element := r.bashPPResolvedArrayLengths(x.Element)
		if element == x.Element {
			return x
		}
		resolved := *x
		resolved.Element = element
		return &resolved
	}
	return typ
}

// bashPPPredeclaredAliases spells the predeclared aliases by the types they
// name — byte is uint8, rune is int32, and any is interface{}, wherever they
// occur in a type — so `byte(1)` is a uint8 to a type switch and []byte is
// []uint8 to an assignment. A declaration of any alias name in the program
// shadows the predeclared one and is left as declared.
func (r *Runner) bashPPPredeclaredAliases(typ syntax.BashPPTypeExpr) syntax.BashPPTypeExpr {
	if typ == nil {
		return nil
	}
	// The result is only ever read, so a type that spells neither alias is
	// returned as it is rather than as a copy: this check runs on every
	// typed assignment.
	text := bashPPTypeText(typ)
	aliases := bashPPPredeclaredAliasTypes
	for alias := range bashPPPredeclaredAliasTypes {
		if _, declared := r.bashPPTypes[alias]; declared {
			if len(aliases) == len(bashPPPredeclaredAliasTypes) {
				aliases = maps.Clone(aliases)
			}
			delete(aliases, alias)
		}
	}
	mentioned := false
	for alias := range aliases {
		if strings.Contains(text, alias) {
			mentioned = true
			break
		}
	}
	if !mentioned {
		return typ
	}
	return bashPPSubstituteType(typ, aliases)
}

// bashPPPredeclaredAliasTypes are the types the predeclared aliases name.
// The nodes are shared and never written.
var bashPPPredeclaredAliasTypes = map[string]syntax.BashPPTypeExpr{
	"byte": &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "uint8"}},
	"rune": &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "int32"}},
	"any":  &syntax.BashPPInterfaceType{},
}

func (r *Runner) bashPPCanonicalAssignableType(typ syntax.BashPPTypeExpr) syntax.BashPPTypeExpr {
	// An alias is transparent through a pointer too: `*Eint` is the same type
	// as `*E[int]`, so resolve the element before the pointer is compared.
	if ptr, ok := typ.(*syntax.BashPPPointerType); ok {
		return &syntax.BashPPPointerType{Star: ptr.Star, Element: r.bashPPCanonicalAssignableType(ptr.Element)}
	}
	seen := make(map[string]bool)
	for {
		name, ok := typ.(*syntax.BashPPNamedType)
		if !ok {
			return typ
		}
		key := name.Name.Value
		if scope, known := r.goSourceLocalTypeScope(name); known {
			key += "@" + scope
		}
		if seen[key] {
			return typ
		}
		decl, found := r.bashPPTypeDeclarationForReference(name)
		if !found || !decl.alias || decl.typeExpr == nil {
			return typ
		}
		seen[key] = true
		typ = bashPPInstantiateTypeDeclaration(name, decl)
	}
}

// Alias references retain their declaration position. A local type may hide
// the registry entry while a package alias still refers to that older entry.
// Select by lexical declaration identity from the existing shadow stack.
func (r *Runner) bashPPTypeDeclarationForReference(name *syntax.BashPPNamedType) (bashPPType, bool) {
	decl, found := r.bashPPTypes[name.Name.Value]
	scope, known := r.goSourceLocalTypeScope(name)
	if !found || !known {
		return decl, found
	}
	matches := func(candidate bashPPType) bool {
		if candidate.typeExpr == nil {
			return false
		}
		ref := &syntax.BashPPNamedType{Name: &syntax.Lit{Value: name.Name.Value, ValuePos: candidate.typeExpr.Pos()}}
		candidateScope, known := r.goSourceLocalTypeScope(ref)
		return known && candidateScope == scope
	}
	if matches(decl) {
		return decl, true
	}
	for i := len(r.bashPPShadowedTypes) - 1; i >= 0; i-- {
		previous := r.bashPPShadowedTypes[i]
		if previous.name == name.Name.Value && previous.prev != nil && matches(*previous.prev) {
			return *previous.prev, true
		}
	}
	return bashPPType{}, false
}

func (r *Runner) bashPPEvalCollection(lit *syntax.BashPPCompositeLit, expected syntax.BashPPTypeExpr) (any, *bashPPCollectionMeta, error) {
	typ := lit.LitType
	if typ == nil {
		typ = expected
	}
	shape := r.bashPPUnderlyingType(typ)
	collection, ok := shape.(*syntax.BashPPCollectionType)
	if !ok {
		return nil, nil, fmt.Errorf("BASHPP-ECOLLECTION-TYPE: collection literal requires array, slice, or map type; got %s", bashPPTypeText(typ))
	}
	if err := r.bashPPValidateCollectionType(collection); err != nil {
		return nil, nil, err
	}
	inferredToArray := false
	if expected != nil && lit.LitType != nil {
		if actualCollection := collection; actualCollection.Kind == "inferred-array" {
			if expectedCollection, ok := r.bashPPUnderlyingType(expected).(*syntax.BashPPCollectionType); ok && expectedCollection.Kind == "array" &&
				(bashPPTypeText(actualCollection.Element) == bashPPTypeText(expectedCollection.Element) ||
					r.goSourceImportTypesIdentical(actualCollection.Element, expectedCollection.Element)) {
				inferredToArray = true
			}
		}
	}
	if expected != nil && lit.LitType != nil && !inferredToArray && !r.bashPPTypeAssignable(typ, expected) {
		return nil, nil, fmt.Errorf("BASHPP-ECOLLECTION-ELEMENT: cannot use %s as %s element", bashPPTypeText(typ), bashPPTypeText(expected))
	}
	meta := &bashPPCollectionMeta{kind: collection.Kind, typ: typ}
	if collection.Kind == "map" {
		if !r.bashPPMapKeyType(collection.Key) {
			return nil, nil, fmt.Errorf("BASHPP-ECOLLECTION-KEY: unsupported map key type %s", bashPPTypeText(collection.Key))
		}
		out := make(map[string]any, len(lit.Elems))
		meta.mapping = make(map[string]*bashPPCollectionMeta, len(lit.Elems))
		meta.mapKeys = make(map[bashPPMapKey]*bashPPMapEntry, len(lit.Elems))
		for _, elem := range lit.Elems {
			if elem.Key == nil {
				return nil, nil, fmt.Errorf("BASHPP-ECOLLECTION-MAP-ELEMENT: map literal element requires key:value")
			}
			key, keyMeta, err := r.bashPPEvalElement(elem.Key, collection.Key)
			if err != nil {
				return nil, nil, fmt.Errorf("BASHPP-ECOLLECTION-KEY: %w", err)
			}
			if _, _, exists, err := r.bashPPSprint165MapLookup(meta, key, keyMeta, collection.Key); err != nil {
				return nil, nil, err
			} else if exists && !r.bashPPGoSource {
				return nil, nil, fmt.Errorf("BASHPP-ECOLLECTION-DUPLICATE: duplicate map key %q", fmt.Sprint(key))
			}
			value, child, err := r.bashPPEvalElement(elem.Value, collection.Element)
			if err != nil {
				return nil, nil, err
			}
			if _, err := r.bashPPSprint165MapStore(out, meta, key, keyMeta, collection.Key, value, child); err != nil {
				return nil, nil, err
			}
		}
		return out, meta, nil
	}

	fixed := -1
	if collection.Kind == "array" {
		var err error
		fixed, err = r.bashPPArrayLength(collection.Length.Value)
		if err != nil || fixed < 0 {
			return nil, nil, fmt.Errorf("BASHPP-ECOLLECTION-LENGTH: invalid array length %s", collection.Length.Value)
		}
	}
	values := make(map[int]any, len(lit.Elems))
	children := make(map[int]*bashPPCollectionMeta, len(lit.Elems))
	next, high := 0, -1
	for _, elem := range lit.Elems {
		index := next
		if elem.Key != nil {
			key, err := r.bashPPCollectionIndex(elem.Key)
			if err != nil {
				return nil, nil, err
			}
			if !key.fitsInt {
				return nil, nil, fmt.Errorf("BASHPP-ECOLLECTION-BOUNDS: index %s out of bounds for %s", key.text, bashPPTypeText(typ))
			}
			index = key.value
		}
		if index < 0 || fixed >= 0 && index >= fixed {
			return nil, nil, fmt.Errorf("BASHPP-ECOLLECTION-BOUNDS: index %d out of bounds for %s", index, bashPPTypeText(typ))
		}
		if _, exists := values[index]; exists {
			return nil, nil, fmt.Errorf("BASHPP-ECOLLECTION-DUPLICATE: duplicate index %d", index)
		}
		value, child, err := r.bashPPEvalElement(elem.Value, collection.Element)
		if err != nil {
			return nil, nil, err
		}
		values[index], children[index] = value, child
		if index > high {
			high = index
		}
		next = index + 1
	}
	length := high + 1
	if fixed >= 0 {
		length = fixed
	}
	if inferredToArray {
		expectedCollection := r.bashPPUnderlyingType(expected).(*syntax.BashPPCollectionType)
		want, err := r.bashPPArrayLength(expectedCollection.Length.Value)
		if err != nil || length != want {
			return nil, nil, fmt.Errorf("BASHPP-ECOLLECTION-LENGTH: cannot use inferred array length %d as %s", length, bashPPTypeText(expected))
		}
		meta.kind = "array"
		meta.typ = expected
		length = want
	}
	out := make([]any, length)
	meta.sequence = make([]*bashPPCollectionMeta, length)
	// Scalar zero values are immutable. Resolve one once and reuse it across
	// the backing array; resolving a named element type for every implicit
	// element makes sparse large literals proportional to their length in type
	// resolution as well as storage. Structured values still need independent
	// payload and metadata per element to preserve Go's value semantics.
	// Only implicit elements need one: an explicit element replaces it, and
	// a native element type's zero is a dependency round trip.
	var zero any
	var zeroMeta *bashPPCollectionMeta
	resolved := false
	for i := range out {
		if _, explicit := values[i]; explicit {
			continue
		}
		if !resolved {
			zero, zeroMeta = r.bashPPZeroValue(collection.Element)
			resolved = true
			out[i], meta.sequence[i] = zero, zeroMeta
			continue
		}
		if zeroMeta == nil {
			out[i] = zero
		} else {
			out[i], meta.sequence[i] = r.bashPPZeroValue(collection.Element)
		}
	}
	for i, value := range values {
		out[i], meta.sequence[i] = value, children[i]
	}
	if collection.Kind == "array" {
		meta.hasValueElements = r.bashPPTypeHasValueElements(collection.Element)
		meta.hasValueElementsKnown = true
	}
	return out, meta, nil
}

func (r *Runner) bashPPValidateCollectionType(typ syntax.BashPPTypeExpr) error {
	return r.bashPPValidateTypeRepresentation(typ, make(map[string]bool), make(map[string]bool))
}

func (r *Runner) bashPPCollectionZero(typ syntax.BashPPTypeExpr) (any, *bashPPCollectionMeta) {
	switch x := typ.(type) {
	case *syntax.BashPPNamedType:
		if shape, ok := r.bashPPUnderlyingType(typ).(*syntax.BashPPCollectionType); ok {
			value, meta := r.bashPPCollectionZero(shape)
			if meta != nil {
				meta.typ = typ
				if shape.Kind == "array" && shape.Element != nil {
					meta.hasValueElements = r.bashPPTypeHasValueElements(shape.Element)
					meta.hasValueElementsKnown = true
				}
			}
			return value, meta
		}
		name := x.Name.Value
		if decl, ok := r.bashPPTypes[name]; ok {
			name = strings.TrimPrefix(decl.underlying, "*")
		}
		switch {
		case name == "string":
			return "", nil
		case name == "bool":
			return false, nil
		case name == "float32" || name == "float64":
			return float64(0), nil
		default:
			return 0, nil
		}
	case *syntax.BashPPCollectionType:
		meta := &bashPPCollectionMeta{kind: x.Kind, typ: x}
		if x.Kind == "map" {
			// Keep a typed nil payload. expand.NewObject(nil) deliberately turns
			// an absent interface into invalidObject, but a nil Go map is still a
			// present, typed value which may be read, ranged over, or compared to
			// nil.
			return map[string]any(nil), meta
		}
		length := 0
		if x.Kind == "array" {
			length, _ = r.bashPPArrayLength(x.Length.Value)
		} else {
			// As with maps, the zero slice is nil but not an absent object. Its
			// typed payload also gives slicing and the builtins a consistent
			// zero-length/capacity representation.
			return []any(nil), meta
		}
		values := make([]any, length)
		meta.sequence = make([]*bashPPCollectionMeta, length)
		if length == 0 {
			meta.hasValueElements = false
			meta.hasValueElementsKnown = true
			return values, meta
		}
		// Resolve the element type once. An immutable scalar zero with no
		// metadata (string, bool, int, float64) is shared by every element;
		// anything carrying metadata or mutable storage is built per element.
		values[0], meta.sequence[0] = r.bashPPZeroValue(x.Element)
		if meta.sequence[0] == nil && bashPPImmutableScalarZero(values[0]) {
			for i := 1; i < length; i++ {
				values[i] = values[0]
			}
			meta.hasValueElements = false
			meta.hasValueElementsKnown = true
			return values, meta
		}
		if x.Kind == "array" {
			meta.hasValueElements = r.bashPPTypeHasValueElements(x.Element)
			meta.hasValueElementsKnown = true
		}
		for i := 1; i < length; i++ {
			values[i], meta.sequence[i] = r.bashPPZeroValue(x.Element)
		}
		return values, meta
	case *syntax.BashPPPointerType:
		return nil, bashPPPointerMeta(x)
	}
	return nil, nil
}

// bashPPImmutableScalarZero reports whether a zero value is a plain immutable
// Go scalar that array elements may share instead of re-deriving per element.
func bashPPImmutableScalarZero(value any) bool {
	switch value.(type) {
	case string, bool, int, float64:
		return true
	}
	return false
}

func (r *Runner) bashPPArrayLength(text string) (int, error) {
	if n, err := strconv.Atoi(text); err == nil {
		return n, nil
	}
	expr, err := goparser.ParseExpr(text)
	if err != nil {
		return 0, err
	}
	value, ok := r.bashPPEvalConstIntExpr(expr)
	if !ok || value.Kind() != constant.Int {
		return 0, fmt.Errorf("not an integer constant")
	}
	n, exact := constant.Int64Val(value)
	if !exact || int64(int(n)) != n {
		return 0, fmt.Errorf("integer constant overflows int")
	}
	return int(n), nil
}

// bashPPConstArrayLen resolves `len(a)` to the constant length of an array
// operand. Only an identifier bound to an array (or inferred-length array)
// qualifies; a slice or map length is a runtime value, not a constant.
func (r *Runner) bashPPConstArrayLen(call *goast.CallExpr) (int, bool) {
	fun, ok := call.Fun.(*goast.Ident)
	if !ok || fun.Name != "len" || len(call.Args) != 1 || r.bashPPScope == nil {
		return 0, false
	}
	ident, ok := call.Args[0].(*goast.Ident)
	if !ok {
		return 0, false
	}
	meta := bashPPCellMeta(r.bashPPScope.lookup(ident.Name))
	if meta == nil || (meta.kind != "array" && meta.kind != "inferred-array") {
		return 0, false
	}
	return len(meta.sequence), true
}

func (r *Runner) bashPPEvalConstIntExpr(expr goast.Expr) (value constant.Value, ok bool) {
	defer func() {
		if recover() != nil {
			value, ok = nil, false
		}
	}()
	switch x := expr.(type) {
	case *goast.CallExpr:
		// `unsafe.Sizeof`/`unsafe.Alignof` are compile-time uintptr constants
		// read from the operand's static type — the only call form an integer
		// constant expression (here, an array length) may legitimately contain.
		if value, ok := r.bashPPUnsafeConstOperator(x); ok {
			return value, true
		}
		// `len(a)` where a is an array is a constant equal to the array's
		// length (Go spec: the expression is not evaluated). This lets an
		// array-of-arrays literal size its inner arrays, as in complit.go's
		// `[][len(at)]*T`. Go-source only; a slice or map len is not constant.
		if r.bashPPGoSource {
			if n, ok := r.bashPPConstArrayLen(x); ok {
				return constant.MakeInt64(int64(n)), true
			}
		}
		return nil, false
	case *goast.BasicLit:
		switch x.Kind {
		case gotoken.INT:
			v := constant.MakeFromLiteral(x.Value, gotoken.INT, 0)
			return v, v.Kind() == constant.Int
		case gotoken.FLOAT:
			// A floating constant is a legal array length when it holds no
			// fractional part, exactly as Go treats `[1e1]int` as `[10]int`.
			// Go-source only: Classic Bash# keeps its integer-literal length.
			if !r.bashPPGoSource {
				return nil, false
			}
			v := constant.MakeFromLiteral(x.Value, gotoken.FLOAT, 0)
			if v.Kind() != constant.Float {
				return nil, false
			}
			if i := constant.ToInt(v); i.Kind() == constant.Int {
				return i, true
			}
		}
		return nil, false
	case *goast.Ident:
		cell := r.bashPPScope.lookup(x.Name).view()
		if cell == nil || !cell.constant {
			return nil, false
		}
		v := bashPPScalarFromString(cell.vr.String()).value
		return v, v != nil && v.Kind() == constant.Int
	case *goast.ParenExpr:
		return r.bashPPEvalConstIntExpr(x.X)
	case *goast.UnaryExpr:
		v, ok := r.bashPPEvalConstIntExpr(x.X)
		if !ok {
			return nil, false
		}
		switch x.Op {
		case gotoken.ADD, gotoken.SUB, gotoken.XOR:
			return constant.UnaryOp(x.Op, v, 0), true
		}
	case *goast.BinaryExpr:
		left, lok := r.bashPPEvalConstIntExpr(x.X)
		right, rok := r.bashPPEvalConstIntExpr(x.Y)
		if !lok || !rok {
			return nil, false
		}
		if x.Op == gotoken.SHL || x.Op == gotoken.SHR {
			shift, exact := constant.Uint64Val(right)
			if !exact {
				return nil, false
			}
			return constant.Shift(left, x.Op, uint(shift)), true
		}
		switch x.Op {
		case gotoken.ADD, gotoken.SUB, gotoken.MUL, gotoken.QUO, gotoken.REM,
			gotoken.AND, gotoken.OR, gotoken.XOR, gotoken.AND_NOT:
			if (x.Op == gotoken.QUO || x.Op == gotoken.REM) && constant.Sign(right) == 0 {
				return nil, false
			}
			return constant.BinaryOp(left, x.Op, right), true
		}
	}
	return nil, false
}

func (r *Runner) bashPPEvalElement(expr syntax.BashPPExpr, expected syntax.BashPPTypeExpr) (any, *bashPPCollectionMeta, error) {
	// Collection destinations written in a generic body are parsed before the
	// function's type arguments are known. Resolve that contextual type at the
	// value boundary, while the active frame still owns the bindings. This is
	// deliberately destination-only: the source cell and its metadata remain
	// intact for structured keys, arrays, pointers, and interface values.
	expected = r.bashPPBindTypeExpr(expected)
	// Imported concrete values can implement an imported interface element.
	// Keep their native handle and let the existing typed assignment check
	// validate the actual type, instead of attempting scalar evaluation.
	if r.bashPPGoSource && r.bashPPNativeType(expected) && r.bashPPNativeExpr(expr) {
		return r.bashPPEvalTypedValue(expr, expected)
	}
	if value, meta, handled, err := r.bashPPSprint165StoredBridgeScalar(expr, expected); handled {
		return value, meta, err
	}
	// A channel element is claimed first: every other reading of the value —
	// as a call result, as a scalar — discards the identity that IS the
	// channel. See [Runner.goSourceChannelElement].
	if value, meta, handled, err := r.goSourceChannelElement(expr, expected); handled {
		return value, meta, err
	}
	if conversion, ok := expr.(*syntax.BashPPConvertExpr); ok && r.bashPPGoSource {
		if value, meta, handled, err := r.bashPPConvertToCollection(conversion); handled {
			if err != nil {
				return nil, nil, err
			}
			if err := r.bashPPCheckTypedValue(value, meta, expected); err != nil {
				return nil, nil, err
			}
			return value, meta, nil
		}
	}

	// A call stored where an interface belongs is an interface conversion of
	// its result — `[]Expr{p.expr()}`, `[]Expr{x, mk()}` — exactly as the same
	// call assigned to an interface variable is; reading its result cell as a
	// bare collection value would drop the box around a pointer result.
	if _, call := expr.(*syntax.BashPPCall); call {
		if handled, err := r.goSourceInterfaceElement(expr, expected); handled {
			if err != nil {
				return nil, nil, err
			}
			return r.bashPPEvalTypedValue(expr, expected)
		}
	}
	if value, meta, handled, err := r.goSourceCollectionCallValue(expr, expected); handled {
		if err != nil {
			return nil, nil, err
		}
		// A call returning a function value — `[]func(){Assert[int]()}`,
		// `[]RO{Bad()}` — transports its closure handle in the result cell's
		// scalar slot. Bind it to the func-typed element before the typed
		// check would reject the handle as a plain scalar.
		if stored, claimed, err := r.bashPPCollectionFuncElement(value, expected); claimed {
			return stored, nil, err
		}
		if bridged, bridgedMeta, claimed, err := r.bashPPCollectionBridgeValue(value, expected); claimed {
			return bridged, bridgedMeta, err
		}
		if err := r.bashPPCheckTypedValue(value, meta, expected); err != nil {
			return nil, nil, err
		}
		if _, native := value.(*bashPPBridgeValue); native && r.bashPPNativeType(expected) {
			meta = &bashPPCollectionMeta{kind: "native", typ: expected}
		}
		value, meta = bashPPCopyArrayValue(value, meta)
		return value, meta, nil
	}
	if ptr, pointer := r.bashPPPointerType(expected); pointer {
		// In an array, slice, or map literal, an element or key of pointer
		// type elides the &T of &T{...}: []*R{{0}} is []*R{&R{0}}. Restore
		// the address-of form so the pointee is allocated like any &-literal.
		if lit, ok := expr.(*syntax.BashPPCompositeLit); ok && lit.LitType == nil && r.bashPPGoSource {
			pointee := *lit
			pointee.LitType = ptr.Element
			return r.bashPPEvalTypedValue(&syntax.BashPPAddressExpr{Amp: lit.Pos(), X: &pointee}, expected)
		}
		return r.bashPPEvalTypedValue(expr, expected)
	}
	if _, deref := expr.(*syntax.BashPPDerefExpr); deref {
		return r.bashPPEvalTypedValue(expr, expected)
	}
	if value, meta, handled, err := r.goSourceNilElement(expr, expected); handled {
		return value, meta, err
	}
	if handled, err := r.goSourceInterfaceElement(expr, expected); handled {
		if err != nil {
			return nil, nil, err
		}
		return r.bashPPEvalTypedValue(expr, expected)
	}
	if r.bashPPGoSource {
		if _, composite := expr.(*syntax.BashPPCompositeLit); composite {
			if _, iface := r.bashPPInterfaceType(expected); iface {
				return r.bashPPEvalTypedValue(expr, expected)
			}
		}
	}
	if lit, ok := expr.(*syntax.BashPPCompositeLit); ok {
		if r.bashPPNativeType(expected) {
			// An element literal may omit its type when the enclosing collection
			// supplies it, as in []testing.InternalTest{{Name: "x", F: f}}.
			// Give the dependency constructor that contextual imported type.
			nativeLit := *lit
			nativeLit.LitType = expected
			value, err := r.bashPPNativeComposite(&nativeLit, false)
			if err != nil {
				return nil, nil, err
			}
			return &value, &bashPPCollectionMeta{kind: "native", typ: expected}, nil
		}
		return r.bashPPEvalComposite(lit, expected)
	}
	if _, indexed := expr.(*syntax.BashPPIndexExpr); indexed {
		value, meta, err := r.bashPPReadExpr(expr)
		if err != nil {
			return nil, nil, err
		}
		if err := r.bashPPCheckTypedValue(value, meta, expected); err != nil {
			return nil, nil, err
		}
		value, meta = bashPPCopyArrayValue(value, meta)
		return value, meta, nil
	}
	if _, sliced := expr.(*syntax.BashPPSliceExpr); sliced {
		value, meta, err := r.bashPPReadExpr(expr)
		if err != nil {
			return nil, nil, err
		}
		if err := r.bashPPCheckTypedValue(value, meta, expected); err != nil {
			return nil, nil, err
		}
		return value, meta, nil
	}
	// A method value — `Config{TearDown: res.teardown}` — is the closure
	// the callable path below binds, not a field to read. A method
	// expression — `[]…{(*state).one}` naming a type's method — is likewise
	// the forwarding closure that path binds; reading its `(*state)` operand
	// as a value would ask for a pointer the type name is not.
	if sel, selected := expr.(*syntax.BashPPSelectorExpr); selected && !(sel.MethodValue && r.bashPPGoSource && !r.bashPPNativeExpr(sel.X)) && !r.goSourceMethodExprSelector(sel) {
		value, meta, err := r.bashPPReadExpr(expr)
		if err != nil {
			return nil, nil, err
		}
		if err := r.bashPPCheckTypedValue(value, meta, expected); err != nil {
			return nil, nil, err
		}
		value, meta = bashPPCopyArrayValue(value, meta)
		return value, meta, nil
	}
	if r.bashPPGoSource {
		// A composite destination cannot be served by the scalar fallback, so
		// a name or type assertion carrying a collection or a struct is read
		// whole — this is what admits an array- or struct-typed variable or
		// assertion as a map key or as a collection element.
		switch expr.(type) {
		case *syntax.BashPPIdent, *syntax.BashPPTypeAssertExpr:
			composite := false
			switch r.bashPPUnderlyingType(expected).(type) {
			case *syntax.BashPPCollectionType, *syntax.BashPPStructType:
				composite = true
			}
			if composite {
				value, meta, err := r.bashPPReadExpr(expr)
				if err != nil {
					return nil, nil, err
				}
				if native, ok := value.(*bashPPBridgeValue); ok {
					converted, convertedMeta, handled, err := r.goSourceNativeSequenceContents(native, expected)
					if err != nil {
						return nil, nil, err
					}
					if handled {
						value, meta = converted, convertedMeta
					}
				}
				if err := r.bashPPCheckTypedValue(value, meta, expected); err != nil {
					return nil, nil, err
				}
				value, meta = bashPPCopyArrayValue(value, meta)
				return value, meta, nil
			}
		}
	}
	if cell, handled, err := r.goSourceCallableCell(expr); handled {
		if err != nil {
			return nil, nil, fmt.Errorf("BASHPP-ECOLLECTION-ELEMENT: %w", err)
		}
		if _, ok := r.bashPPUnderlyingType(expected).(*syntax.BashPPFuncType); !ok {
			return nil, nil, fmt.Errorf("BASHPP-ECOLLECTION-ELEMENT: cannot use function as %s", bashPPTypeText(expected))
		}
		stored, claimed, err := r.bashPPCollectionFuncElement(cell.vr.Str, expected)
		if !claimed {
			return nil, nil, fmt.Errorf("BASHPP-ECOLLECTION-ELEMENT: function value is unavailable")
		}
		return stored, nil, err
	}
	scalar, err := r.bashPPEvalScalarExpr(expr)
	if err != nil {
		return nil, nil, fmt.Errorf("BASHPP-ECOLLECTION-ELEMENT: %w", err)
	}
	// go/constant cannot represent NaN or infinity. They travel through scalar
	// evaluation in the side carrier, so collection keys and elements must use
	// that runtime value instead of the carrier's placeholder zero. In
	// particular, each NaN map insertion is a distinct key and a NaN lookup
	// never finds an existing entry.
	if scalar.hasNonFinite {
		var value any = scalar.nonFinite
		if scalar.nanWidth != 0 {
			value = bashPPScalarStorageString(scalar)
		}
		value = r.bashPPContextualCollectionValue(value, expected)
		if err := r.bashPPCheckCollectionValue(value, expected); err != nil {
			return nil, nil, err
		}
		return value, nil, nil
	}
	represented := r.bashPPRepresentableScalar(scalar, expected)
	var value any
	if represented.hasNonFiniteComplex {
		// go/constant has no Inf or NaN representation. Collections retain the
		// exact runtime spelling and reconstruct the tagged scalar on read.
		value = bashPPScalarStorageString(represented)
	} else {
		value = bashPPScalarAny(represented.value)
	}
	value = r.bashPPContextualCollectionValue(value, expected)
	if err := r.bashPPCheckCollectionValue(value, expected); err != nil {
		return nil, nil, err
	}
	return value, nil, nil
}

// bashPPCollectionFuncElement claims a closure handle for a func-typed
// collection element. The value is the handle string a function value travels
// as — the scalar slot of a call's result cell, an appended parameter's cell,
// a named callable — and the element type's underlying signature is what it
// must satisfy. It reports claimed=false when either side is not a function,
// leaving the scalar paths to describe the mismatch.
//
// A closure that is already instantiated — `Assert[int]()` returned
// `assert[To]` at To=int — carries every type argument it needs and is an
// ordinary function value; its substituted signature is compared directly
// rather than re-inferred from a context that mentions no parameter. A
// still-generic declaration is instantiated from the element type as it is
// for a func-typed parameter. The original handle is kept when binding did
// not produce a new closure, so appending a function value does not grow the
// closure table.
func (r *Runner) bashPPCollectionFuncElement(value any, expected syntax.BashPPTypeExpr) (any, bool, error) {
	signature, ok := r.bashPPUnderlyingType(expected).(*syntax.BashPPFuncType)
	if !ok {
		return nil, false, nil
	}
	handle, ok := value.(string)
	if !ok {
		return nil, false, nil
	}
	candidate, ok := r.bashPPClosure(handle)
	if !ok {
		return nil, false, nil
	}
	mismatch := fmt.Errorf("BASHPP-ECOLLECTION-ELEMENT: function does not match %s", bashPPTypeText(expected))
	if bashPPFullyInstantiated(candidate, candidate.typeParams()) {
		if !bashPPSignatureMatches(candidate.params(), candidate.results(), signature) {
			return nil, true, mismatch
		}
		return handle, true, nil
	}
	bound, status := r.bashPPContextualFuncValue(candidate, signature)
	if status != bashPPBindOK {
		return nil, true, mismatch
	}
	if bound == candidate {
		return handle, true, nil
	}
	return r.bashPPStoreFunc(bound).Str, true, nil
}

// bashPPContextualCollectionValue restores the destination-driven conversion
// of an untyped constant. The shell carrier stores some Go-region words as
// strings; when a collection element supplies a numeric destination, parse
// that spelling with the destination's underlying scalar type before the
// ordinary representation check.
func (r *Runner) bashPPContextualCollectionValue(value any, expected syntax.BashPPTypeExpr) any {
	name, ok := r.bashPPUnderlyingType(expected).(*syntax.BashPPNamedType)
	if !ok {
		return value
	}
	text, ok := value.(string)
	if !ok {
		return value
	}
	switch name.Name.Value {
	case "float32", "float64":
		if parsed := constant.MakeFromLiteral(text, gotoken.FLOAT, 0); parsed.Kind() == constant.Float {
			return bashPPScalarAny(parsed)
		}
		parts := strings.Split(text, "/")
		if len(parts) == 2 {
			numerator, nerr := strconv.ParseFloat(parts[0], 64)
			denominator, derr := strconv.ParseFloat(parts[1], 64)
			if nerr == nil && derr == nil && denominator != 0 {
				return numerator / denominator
			}
		}
	case "int", "int8", "int16", "int32", "int64", "uint", "uint8", "uint16", "uint32", "uint64", "uintptr", "byte", "rune":
		if parsed := constant.MakeFromLiteral(text, gotoken.INT, 0); parsed.Kind() == constant.Int {
			return bashPPScalarAny(parsed)
		}
	}
	return value
}

func bashPPScalarAny(value constant.Value) any {
	switch value.Kind() {
	case constant.String:
		return constant.StringVal(value)
	case constant.Bool:
		return constant.BoolVal(value)
	case constant.Int:
		if n, ok := constant.Int64Val(value); ok {
			return int(n)
		}
	case constant.Float:
		// Float64Val's second result is exactness, not success: an untyped
		// 0.1 or a computed 3/10 has no exact float64, and returning its
		// rational text instead would hand `1/10` to fmt through an
		// interface{} element. The nearest float64 is the Go value.
		n, _ := constant.Float64Val(value)
		return n
	}
	return bashPPScalarString(value)
}

func (r *Runner) bashPPCheckCollectionValue(value any, expected syntax.BashPPTypeExpr) error {
	name, ok := expected.(*syntax.BashPPNamedType)
	if !ok {
		return fmt.Errorf("BASHPP-ECOLLECTION-ELEMENT: scalar cannot be used as %s", bashPPTypeText(expected))
	}
	typ := name.Name.Value
	for {
		decl, found := r.bashPPTypes[typ]
		if !found {
			break
		}
		typ = strings.TrimPrefix(decl.underlying, "*")
	}
	valid := false
	switch {
	case typ == "any":
		valid = true
	case typ == "string":
		_, valid = value.(string)
	case typ == "bool":
		_, valid = value.(bool)
	case bashPPIntegerType(typ):
		switch v := value.(type) {
		case int:
			valid = true
		case string:
			// A large unsigned constant (> math.MaxInt64) cannot live in the
			// interpreter's signed int carrier, so it arrives as its decimal
			// spelling. Accept it only when it is a representable integer for
			// this destination — an ordinary string element stays invalid.
			valid = r.bashPPGoSource && bashPPCollectionIntegerText(typ, v)
		}
	case typ == "float32" || typ == "float64":
		switch value.(type) {
		case int, float64:
			valid = true
		case string:
			valid = bashPPCollectionFloatText(value.(string))
		}
	case typ == "complex64" || typ == "complex128":
		switch value.(type) {
		case complex64, complex128:
			valid = true
		default:
			valid = bashPPSprint162ComplexCollectionText(value)
		}
	}
	if !valid {
		return fmt.Errorf("BASHPP-ECOLLECTION-ELEMENT: cannot use %T value as %s", value, name.Name.Value)
	}
	return nil
}

// bashPPCollectionIntegerText reports whether an integer element carried as
// its decimal spelling is a representable value for the destination integer
// type. This is the large-unsigned carrier path (values above math.MaxInt64
// that the signed int carrier cannot hold); it is not permissive string
// parsing, since only a well-formed integer literal within range qualifies.
func bashPPCollectionIntegerText(typ, text string) bool {
	parsed := constant.MakeFromLiteral(text, gotoken.INT, 0)
	if parsed.Kind() != constant.Int {
		return false
	}
	return bashPPIntegerRepresentable(typ, parsed)
}

// bashPPUnderlyingIntegerName resolves a declared type name to the predeclared
// integer type it is built on, following named-type declarations. It reports
// false for any type whose underlying type is not an integer.
func (r *Runner) bashPPUnderlyingIntegerName(name string) (string, bool) {
	seen := make(map[string]bool)
	for {
		decl, found := r.bashPPTypes[name]
		if !found {
			break
		}
		if seen[name] {
			return "", false
		}
		seen[name] = true
		// A pointer to an integer is not an integer carrier. In particular,
		// peeling * from the legal recursive type Peano *Peano loops forever.
		name = decl.underlying
	}
	if bashPPIntegerType(name) {
		return name, true
	}
	return "", false
}

// bashPPStringCarriesInteger reports whether the decimal spelling stored for a
// scalar element is the large-unsigned carrier of an integer declared type —
// a []uint64 element above math.MaxInt64 that could not live in the signed
// int carrier. Only a representable integer literal for the resolved integer
// type qualifies; an ordinary string element (declared string, or a named
// string type) is left as its string carrier.
func (r *Runner) bashPPStringCarriesInteger(typ syntax.BashPPTypeExpr, text string) bool {
	named, ok := r.bashPPUnderlyingType(typ).(*syntax.BashPPNamedType)
	if !ok {
		return false
	}
	dest, ok := r.bashPPUnderlyingIntegerName(named.Name.Value)
	if !ok {
		return false
	}
	return bashPPCollectionIntegerText(dest, text)
}

func bashPPCollectionFloatText(text string) bool {
	if _, ok := bashPPNaNBitsScalar(text, ""); ok {
		return true
	}
	if _, err := strconv.ParseFloat(text, 64); err == nil {
		return true
	}
	parts := strings.Split(text, "/")
	if len(parts) != 2 {
		return false
	}
	_, nerr := strconv.ParseFloat(parts[0], 64)
	denominator, derr := strconv.ParseFloat(parts[1], 64)
	return nerr == nil && derr == nil && denominator != 0
}

// bashPPMapKeyType reports whether typ may encode map keys. A named scalar
// declaration such as `type ServerState int` carries its underlying scalar
// identity: resolving through the declaration keeps key encoding and lookup
// canonical over the same scalar instead of rejecting the named type.
func (r *Runner) bashPPMapKeyType(typ syntax.BashPPTypeExpr) bool {
	// A type parameter is checked where it is instantiated; the constraint
	// (comparable) is what a generic declaration promises about it.
	if _, ok := typ.(*syntax.BashPPTypeParamType); ok {
		return true
	}
	if len(r.bashPPTypeParamArgs) > 0 {
		typ = bashPPSubstituteType(typ, r.bashPPTypeParamArgs)
	}
	if _, ok := r.bashPPInterfaceType(typ); ok {
		return true
	}
	if _, ok := r.bashPPUnderlyingType(typ).(*syntax.BashPPChanType); ok {
		return true
	}
	return r.bashPPComparableType(typ, make(map[string]bool))
}

type bashPPCollectionIndexValue struct {
	value   int
	text    string
	fitsInt bool
	number  constant.Value
}

func (index bashPPCollectionIndexValue) outOfBounds(length int) bool {
	return !index.fitsInt || index.value < 0 || index.value >= length
}

func bashPPCollectionIndexInt(value int) bashPPCollectionIndexValue {
	number := constant.MakeInt64(int64(value))
	return bashPPCollectionIndexValue{value: value, text: number.ExactString(), fitsInt: true, number: number}
}

func (index bashPPCollectionIndexValue) less(other bashPPCollectionIndexValue) bool {
	return constant.Compare(index.number, gotoken.LSS, other.number)
}

func (index bashPPCollectionIndexValue) greaterThan(value int) bool {
	return constant.Compare(index.number, gotoken.GTR, constant.MakeInt64(int64(value)))
}

func (r *Runner) bashPPResolveCollectionIndex(v bashPPScalar, diagnostic string) (bashPPCollectionIndexValue, error) {
	n, ok := constant.Int64Val(v.value)
	if ok && int64(int(n)) == n {
		index := bashPPCollectionIndexInt(int(n))
		return index, nil
	}
	// Go permits every integer type as an index. In particular, a uint64
	// runtime value above MaxInt64 is a valid index expression even though it
	// cannot address an interpreter-owned sequence. Preserve its exact spelling
	// so the inevitable bounds panic names the actual index. Classic Bash# keeps
	// its historical int-only diagnostic.
	if r.bashPPGoSource && v.value.Kind() == constant.Int {
		if ok {
			return bashPPCollectionIndexValue{text: v.value.ExactString(), number: v.value}, nil
		}
		if _, ok := constant.Uint64Val(v.value); ok {
			return bashPPCollectionIndexValue{text: v.value.ExactString(), number: v.value}, nil
		}
	}
	return bashPPCollectionIndexValue{}, fmt.Errorf("%s", diagnostic)
}

func (r *Runner) bashPPCollectionIndex(expr syntax.BashPPExpr) (bashPPCollectionIndexValue, error) {
	v, err := r.bashPPCollectionIndexScalar(expr)
	if err != nil {
		return bashPPCollectionIndexValue{}, fmt.Errorf("BASHPP-ECOLLECTION-INDEX: %w", err)
	}
	return r.bashPPResolveCollectionIndex(v, "BASHPP-ECOLLECTION-INDEX: index must be an integer")
}

func (r *Runner) bashPPCollectionIndexScalar(expr syntax.BashPPExpr) (bashPPScalar, error) {
	if r.bashPPGoSource {
		switch x := expr.(type) {
		case *syntax.BashPPParenExpr:
			return r.bashPPCollectionIndexScalar(x.X)
		case *syntax.BashPPCall:
			if value, handled, err := r.goSourceUnsafeConstant(x); handled {
				return value, err
			}
			cell, err := r.goSourceValueCell(x)
			if err != nil {
				return bashPPScalar{}, err
			}
			return r.bashPPScalarFromCell(cell), nil
		}
	}
	return r.bashPPEvalScalarExpr(expr)
}

func (r *Runner) bashPPSliceBound(expr syntax.BashPPExpr, fallback int) (bashPPCollectionIndexValue, error) {
	if expr == nil {
		return bashPPCollectionIndexInt(fallback), nil
	}
	v, err := r.bashPPCollectionIndexScalar(expr)
	if err != nil {
		return bashPPCollectionIndexValue{}, fmt.Errorf("BASHPP-ECOLLECTION-SLICE: %v", err)
	}
	return r.bashPPResolveCollectionIndex(v, "BASHPP-ECOLLECTION-SLICE: bound must be an integer")
}

// bashPPStringSliceBound evaluates a string slice bound exactly once, reporting
// whether the bound is a runtime value. A runtime-typed bound routes an
// out-of-range string slice to Go's recoverable panic; a constant one keeps the
// front-end diagnostic (the Go-source checker already rejects a wholly constant
// invalid slice, so only the classic dialect reaches that branch here).
func (r *Runner) bashPPStringSliceBound(expr syntax.BashPPExpr, fallback int) (bashPPCollectionIndexValue, bool, error) {
	if expr == nil {
		return bashPPCollectionIndexInt(fallback), false, nil
	}
	v, err := r.bashPPCollectionIndexScalar(expr)
	if err != nil {
		return bashPPCollectionIndexValue{}, false, fmt.Errorf("BASHPP-ECOLLECTION-INDEX: %w", err)
	}
	index, err := r.bashPPResolveCollectionIndex(v, "BASHPP-ECOLLECTION-INDEX: index must be an integer")
	if err != nil {
		return bashPPCollectionIndexValue{}, false, err
	}
	return index, v.runtime, nil
}

func (r *Runner) bashPPSliceBounds(expr *syntax.BashPPSliceExpr, length, capacity int, sliceOperand bool) (int, int, int, error) {
	low, err := r.bashPPSliceBound(expr.Low, 0)
	if err != nil {
		return 0, 0, 0, err
	}
	high, err := r.bashPPSliceBound(expr.High, length)
	if err != nil {
		return 0, 0, 0, err
	}
	max := bashPPCollectionIndexInt(capacity)
	if expr.SecondColon.IsValid() {
		max, err = r.bashPPSliceBound(expr.Max, capacity)
		if err != nil {
			return 0, 0, 0, err
		}
	}
	highLimit := length
	if sliceOperand {
		highLimit = capacity
	}
	invalid := low.less(bashPPCollectionIndexInt(0)) || high.less(low) || high.greaterThan(highLimit) || max.less(high) || max.greaterThan(capacity)
	if invalid {
		if r.bashPPGoSource {
			return 0, 0, 0, r.goSourceSliceBoundsPanic(expr, low, high, max, highLimit, capacity, sliceOperand)
		}
		if expr.SecondColon.IsValid() {
			return 0, 0, 0, fmt.Errorf("BASHPP-ECOLLECTION-SLICE: slice bounds out of range [%s:%s:%s] with length %d and capacity %d", low.text, high.text, max.text, length, capacity)
		}
		return 0, 0, 0, fmt.Errorf("BASHPP-ECOLLECTION-SLICE: slice bounds out of range [%s:%s] with length %d", low.text, high.text, length)
	}
	if !expr.SecondColon.IsValid() {
		max = bashPPCollectionIndexInt(capacity)
	}
	return low.value, high.value, max.value, nil
}

func (r *Runner) bashPPCollectionRead(index *syntax.BashPPIndexExpr) (any, *bashPPCollectionMeta, error) {
	return r.bashPPReadExpr(index)
}

func bashPPCollectionRoot(expr syntax.BashPPExpr) (string, bool) {
	switch x := expr.(type) {
	case *syntax.BashPPParenExpr:
		return bashPPCollectionRoot(x.X)
	case *syntax.BashPPDerefExpr:
		return bashPPCollectionRoot(x.X)
	case *syntax.BashPPIdent:
		return x.Name.Value, true
	case *syntax.BashPPIndexExpr:
		return bashPPCollectionRoot(x.X)
	case *syntax.BashPPSliceExpr:
		return bashPPCollectionRoot(x.X)
	case *syntax.BashPPSelectorExpr:
		return bashPPCollectionRoot(x.X)
	}
	return "", false
}

func bashPPExprText(expr syntax.BashPPExpr) string {
	switch x := expr.(type) {
	case *syntax.BashPPIdent:
		return x.Name.Value
	case *syntax.BashPPBasicLit:
		return x.Value.Value
	case *syntax.BashPPIndexExpr:
		return bashPPExprText(x.X) + "[" + bashPPExprText(x.Index) + "]"
	case *syntax.BashPPSliceExpr:
		low, high, max := "", "", ""
		if x.Low != nil {
			low = bashPPExprText(x.Low)
		}
		if x.High != nil {
			high = bashPPExprText(x.High)
		}
		if x.Max != nil {
			max = bashPPExprText(x.Max)
		}
		if x.SecondColon.IsValid() {
			return bashPPExprText(x.X) + "[" + low + ":" + high + ":" + max + "]"
		}
		return bashPPExprText(x.X) + "[" + low + ":" + high + "]"
	case *syntax.BashPPSelectorExpr:
		return bashPPExprText(x.X) + "." + x.Sel.Value
	}
	return "?"
}

func (r *Runner) bashPPCollectionAssign(target *syntax.BashPPIndexExpr, rhs syntax.BashPPExpr) {
	root, ok := bashPPCollectionRoot(target)
	cell := r.bashPPScope.lookup(root).view()
	rootMeta := bashPPCellMeta(cell)
	if !ok || cell == nil || rootMeta == nil {
		r.errf("BASHPP-ECOLLECTION-ASSIGN: indexed target is not a collection\n")
		r.exit = exitStatus{code: 2}
		return
	}
	path := strings.TrimPrefix(bashPPExprText(target), root)
	if cell.object != nil && cell.object.readonly {
		if root != cell.object.owner {
			r.errf("BASHPP-EREADONLY-MUTATION: cannot mutate readonly value %q through alias %q and path %s\n", cell.object.owner, root, path)
		} else {
			parentMeta := cell.object.collection
			if nested, ok := target.X.(*syntax.BashPPIndexExpr); ok {
				_, parentMeta, _ = r.bashPPCollectionRead(nested)
			}
			kind := "slice"
			if parentMeta != nil && parentMeta.kind == "map" {
				kind = "map"
			}
			r.errf("BASHPP-EREADONLY-MUTATION: cannot mutate readonly value %q through %s path %s\n", cell.object.owner, kind, path)
		}
		r.exit = exitStatus{code: 2}
		return
	}

	var parent any
	var meta *bashPPCollectionMeta
	if id, ok := target.X.(*syntax.BashPPIdent); ok {
		_ = id
		parent, meta = cell.vr.Obj, rootMeta
	} else {
		var err error
		parent, meta, err = r.bashPPCollectionRead(target.X.(*syntax.BashPPIndexExpr))
		if err != nil {
			r.errf("%v\n", err)
			r.exit = exitStatus{code: 2}
			return
		}
	}
	if meta == nil {
		r.errf("BASHPP-ECOLLECTION-ASSIGN: indexed target is not a collection\n")
		r.exit = exitStatus{code: 2}
		return
	}
	typ, ok := r.bashPPUnderlyingType(meta.typ).(*syntax.BashPPCollectionType)
	if !ok {
		r.errf("BASHPP-ECOLLECTION-ASSIGN: indexed target is not a collection\n")
		r.exit = exitStatus{code: 2}
		return
	}
	value, child, err := r.bashPPEvalElement(rhs, typ.Element)
	if err != nil {
		r.errf("%v\n", err)
		r.exit = exitStatus{code: 2}
		return
	}
	if meta.kind == "map" {
		if parent == nil {
			r.bashPPReportFault(errBashPPNilMapAssign)
			return
		}
		key, keyMeta, keyErr := r.bashPPEvalElement(target.Index, typ.Key)
		if keyErr != nil {
			r.errf("BASHPP-ECOLLECTION-KEY: %v\n", keyErr)
			r.exit = exitStatus{code: 2}
			return
		}
		if _, err := r.bashPPSprint165MapStore(parent.(map[string]any), meta, key, keyMeta, typ.Key, value, child); err != nil {
			r.errf("%v\n", err)
			r.exit = exitStatus{code: 2}
		}
		return
	}
	index, indexErr := r.bashPPCollectionIndex(target.Index)
	if indexErr != nil {
		r.errf("%v\n", indexErr)
		r.exit = exitStatus{code: 2}
		return
	}
	sequence := parent.([]any)
	if index.outOfBounds(len(sequence)) {
		if r.bashPPGoSource {
			// Go's indexed assignment faults at runtime, recoverably; the
			// classic diagnostic stays the abort Bash# always reported.
			r.bashPPSprint162CollectionBoundsPanic(target, index, len(sequence))
			return
		}
		r.errf("BASHPP-ECOLLECTION-BOUNDS: index %s out of bounds for length %d\n", index.text, len(sequence))
		r.exit = exitStatus{code: 2}
		return
	}
	sequence[index.value], meta.sequence[index.value] = value, child
	if child != nil && (bashPPValueMeta(child) || child.interfaceValue != nil) {
		meta.hasValueElements = true
		meta.hasValueElementsKnown = true
	}
}
