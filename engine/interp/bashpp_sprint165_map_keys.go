// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// bashPPMapKey is the evaluator's comparable representation of a Go map key.
// The payload remains JSON-shaped for shell interoperability, while this key
// preserves the Go type and equality identity that a string property cannot.
type bashPPMapKey struct {
	typ   string
	value string
	nonce uint64
}

type bashPPMapEntry struct {
	storage string
	key     any
	keyMeta *bashPPCollectionMeta
	// dead is set when the entry is deleted from its map, so a cached
	// iteration order can skip and compact it.
	dead bool
}

func (r *Runner) bashPPSprint165StoredBridgeScalar(expr syntax.BashPPExpr, expected syntax.BashPPTypeExpr) (any, *bashPPCollectionMeta, bool, error) {
	id, ok := expr.(*syntax.BashPPIdent)
	if !ok || r.bashPPScope == nil {
		return nil, nil, false, nil
	}
	// One snapshot answers the whole question: the kind test, the bridge-value
	// assertion and the value handed on all have to describe ONE store.
	vr := r.bashPPScope.lookup(id.Name.Value).viewVar()
	if vr.Kind != expand.Object {
		return nil, nil, false, nil
	}
	native, ok := vr.Obj.(*bashPPBridgeValue)
	if !ok || native == nil {
		return nil, nil, false, nil
	}
	switch native.Kind {
	case "string", "bool", "int", "uint", "float", "complex":
	default:
		return nil, nil, false, nil
	}
	return r.bashPPCollectionBridgeValue(vr.Obj, expected)
}

func bashPPSprint165MapPart(text string) string {
	return strconv.Itoa(len(text)) + ":" + text
}

func bashPPSprint165PointerMapValue(pointer *bashPPPointer) string {
	if pointer == nil {
		return "nil"
	}
	if pointer.storageAddress != nil {
		return fmt.Sprintf("storage:%p", pointer.storageAddress)
	}
	var path strings.Builder
	for _, step := range pointer.path {
		path.WriteString(bashPPSprint165MapPart(step.field))
		path.WriteByte('/')
		path.WriteString(strconv.Itoa(step.index))
		path.WriteByte('/')
		path.WriteString(strconv.FormatBool(step.deref))
	}
	return fmt.Sprintf("%p/%s", pointer.target, path.String())
}

func bashPPSprint165MapStorageName(value any, meta *bashPPCollectionMeta, nonce uint64) string {
	if meta != nil && (meta.interfaceValue != nil || meta.channel != nil) {
		return "#map" + strconv.FormatUint(nonce, 10)
	}
	switch value := value.(type) {
	case string:
		return value
	case bool, int, int64, float64, complex64, complex128:
		return fmt.Sprint(value)
	case *bashPPBridgeValue:
		if value != nil {
			return "#map" + strconv.FormatUint(nonce, 10)
		}
	case *bashPPPointer:
		return "#map" + strconv.FormatUint(nonce, 10)
	case map[string]any, []any:
		return "#map" + strconv.FormatUint(nonce, 10)
	}
	return fmt.Sprint(value)
}

func (r *Runner) bashPPSprint165MapKey(value any, meta *bashPPCollectionMeta, typ syntax.BashPPTypeExpr) (bashPPMapKey, bool, error) {
	if meta != nil && meta.interfaceValue != nil {
		iface := meta.interfaceValue
		if iface.nilIface || iface.cell == nil {
			return bashPPMapKey{typ: "<nil>", value: "nil"}, false, nil
		}
		dynamic := iface.dynamic
		if !r.bashPPMapKeyType(dynamic) {
			r.bashPPRaise("runtime error: hash of unhashable type " + r.goSourceRuntimeTypeName(dynamic))
			return bashPPMapKey{}, false, errBashPPScalarInterrupted
		}
		cell := iface.cell
		var inner any
		var innerMeta *bashPPCollectionMeta
		switch {
		case cell.pointer:
			inner, innerMeta = cell.pointerValue, bashPPPointerMeta(cell.declType)
		case cell.vr.Kind == expand.Object:
			inner, innerMeta = cell.vr.Obj, bashPPCellMeta(cell)
		default:
			inner = bashPPBuiltinExactScalarValue(cell.vr.String(), r.bashPPScalarFromCell(cell))
		}
		key, nonreflexive, err := r.bashPPSprint165MapKey(inner, innerMeta, dynamic)
		// An interface-keyed map hashes the represented value together with its
		// dynamic Go type. In particular, instantiated arguments retain the
		// complete identity of anonymous structs (including tags) and local
		// named types; the display-oriented type text intentionally abbreviates
		// those shapes and is therefore not a map-key identity.
		key.typ = r.goSourceDynamicTypeIdentity(dynamic)
		return key, nonreflexive, err
	}

	typeName := bashPPTypeText(typ)
	shape := r.bashPPUnderlyingType(typ)
	switch x := shape.(type) {
	case *syntax.BashPPNamedType:
		if native, ok := value.(*bashPPBridgeValue); ok && native != nil {
			decoded, _, err := bashPPBridgeScalarValue(*native)
			if err != nil {
				return bashPPMapKey{}, false, err
			}
			value = decoded
		}
		kind := x.Name.Value
		if !bashPPBuiltinType(kind) {
			if base, ok := r.goSourceImportedScalarUnderlying(kind); ok {
				// A dependency-owned defined type resolves no further in the
				// interpreter registry; its basic underlying kind comes from
				// the import's export metadata. Its identity is the one the
				// dynamic-type path spells (import path, not per-file alias),
				// so a delete or lookup through this branch hashes the same
				// key an interface-boxed store of the same value produced.
				kind = base
				typeName = r.goSourceDynamicTypeIdentity(typ)
			}
		}
		switch kind {
		case "string":
			text, ok := value.(string)
			if !ok {
				return bashPPMapKey{}, false, fmt.Errorf("BASHPP-ECOLLECTION-KEY: %T is not %s", value, typeName)
			}
			return bashPPMapKey{typ: typeName, value: "s" + bashPPSprint165MapPart(text)}, false, nil
		case "bool":
			boolean, ok := value.(bool)
			if !ok {
				return bashPPMapKey{}, false, fmt.Errorf("BASHPP-ECOLLECTION-KEY: %T is not %s", value, typeName)
			}
			return bashPPMapKey{typ: typeName, value: "b" + strconv.FormatBool(boolean)}, false, nil
		case "float32", "float64":
			// A NaN key retains its IEEE spelling in the map entry so ranging
			// over the map can recover its payload. Hashing only needs the
			// nonreflexive float value, regardless of its payload.
			if text, isText := value.(string); isText {
				if nan, isBits := bashPPNaNBitsScalar(text, kind); isBits {
					value = nan.nonFinite
				}
			}
			floating, ok := value.(float64)
			if !ok {
				if integer, integerOK := value.(int); integerOK {
					floating, ok = float64(integer), true
				}
			}
			if !ok {
				return bashPPMapKey{}, false, fmt.Errorf("BASHPP-ECOLLECTION-KEY: %T is not %s", value, typeName)
			}
			if kind == "float32" {
				floating = float64(float32(floating))
			}
			if math.IsNaN(floating) {
				return bashPPMapKey{typ: typeName, value: "nan"}, true, nil
			}
			if floating == 0 {
				floating = 0
			}
			return bashPPMapKey{typ: typeName, value: "f" + strconv.FormatUint(math.Float64bits(floating), 16)}, false, nil
		case "complex64", "complex128":
			var complexValue complex128
			switch value := value.(type) {
			case complex64:
				complexValue = complex128(value)
			case complex128:
				complexValue = value
			case string:
				// Runtime IEEE components must not pass through go/constant:
				// it cannot represent NaN, infinity, or signed zero.
				parsed, err := strconv.ParseComplex(value, 128)
				if err != nil {
					return bashPPMapKey{}, false, fmt.Errorf("BASHPP-ECOLLECTION-KEY: invalid %s: %w", typeName, err)
				}
				complexValue = parsed
			default:
				return bashPPMapKey{}, false, fmt.Errorf("BASHPP-ECOLLECTION-KEY: %T is not %s", value, typeName)
			}
			if kind == "complex64" {
				complexValue = complex128(complex64(complexValue))
			}
			if math.IsNaN(real(complexValue)) || math.IsNaN(imag(complexValue)) {
				return bashPPMapKey{typ: typeName, value: "nan"}, true, nil
			}
			realPart, imagPart := real(complexValue), imag(complexValue)
			if realPart == 0 {
				realPart = 0
			}
			if imagPart == 0 {
				imagPart = 0
			}
			encoded := strconv.FormatUint(math.Float64bits(realPart), 16) + "/" + strconv.FormatUint(math.Float64bits(imagPart), 16)
			return bashPPMapKey{typ: typeName, value: "c" + encoded}, false, nil
		default:
			if bashPPIntegerType(kind) {
				return bashPPMapKey{typ: typeName, value: "i" + fmt.Sprint(value)}, false, nil
			}
		}
	case *syntax.BashPPPointerType:
		if native, ok := value.(*bashPPBridgeValue); ok {
			if native == nil || native.Kind == "nil" || (native.Handle == 0 && native.Origin == 0) {
				return bashPPMapKey{typ: typeName, value: "nil"}, false, nil
			}
			// Pointer handles remain stable across crossings in one session.
			return bashPPMapKey{typ: typeName, value: fmt.Sprintf("native:%s:%d:%d", native.Session, native.Handle, native.Origin)}, false, nil
		}
		pointer, ok := value.(*bashPPPointer)
		if !ok || pointer == nil {
			return bashPPMapKey{typ: typeName, value: "nil"}, false, nil
		}
		return bashPPMapKey{typ: typeName, value: bashPPSprint165PointerMapValue(pointer)}, false, nil
	case *syntax.BashPPChanType:
		if meta != nil && meta.channel != nil {
			return bashPPMapKey{typ: typeName, value: fmt.Sprintf("local:%p", meta.channel)}, false, nil
		}
		if native, ok := value.(*bashPPBridgeValue); ok && native != nil {
			return bashPPMapKey{typ: typeName, value: fmt.Sprintf("native:%s:%d", native.Session, native.Handle)}, false, nil
		}
		return bashPPMapKey{typ: typeName, value: "nil"}, false, nil
	case *syntax.BashPPCollectionType:
		if x.Kind != "array" {
			break
		}
		sequence, ok := value.([]any)
		if !ok {
			return bashPPMapKey{}, false, fmt.Errorf("BASHPP-ECOLLECTION-KEY: %T is not %s", value, typeName)
		}
		var encoded strings.Builder
		for i, item := range sequence {
			var child *bashPPCollectionMeta
			if meta != nil && i < len(meta.sequence) {
				child = meta.sequence[i]
			}
			part, nonreflexive, err := r.bashPPSprint165MapKey(item, child, x.Element)
			if err != nil {
				return bashPPMapKey{}, false, err
			}
			if nonreflexive {
				return bashPPMapKey{typ: typeName, value: "nan"}, true, nil
			}
			encoded.WriteString(bashPPSprint165MapPart(part.typ))
			encoded.WriteString(bashPPSprint165MapPart(part.value))
		}
		return bashPPMapKey{typ: typeName, value: "a" + encoded.String()}, false, nil
	case *syntax.BashPPStructType:
		mapping, ok := value.(map[string]any)
		if !ok {
			return bashPPMapKey{}, false, fmt.Errorf("BASHPP-ECOLLECTION-KEY: %T is not %s", value, typeName)
		}
		var encoded strings.Builder
		for _, field := range bashPPFlatFields(x.Fields) {
			var child *bashPPCollectionMeta
			if meta != nil {
				child = bashPPLayoutGet(meta.mapping, field.name)
			}
			fieldValue, _ := bashPPStorageGet(mapping, field.name)
			part, nonreflexive, err := r.bashPPSprint165MapKey(fieldValue, child, field.typ)
			if err != nil {
				return bashPPMapKey{}, false, err
			}
			if nonreflexive {
				return bashPPMapKey{typ: typeName, value: "nan"}, true, nil
			}
			encoded.WriteString(bashPPSprint165MapPart(field.name))
			encoded.WriteString(bashPPSprint165MapPart(part.typ))
			encoded.WriteString(bashPPSprint165MapPart(part.value))
		}
		return bashPPMapKey{typ: typeName, value: "r" + encoded.String()}, false, nil
	}
	return bashPPMapKey{}, false, fmt.Errorf("BASHPP-ECOLLECTION-KEY: unsupported map key type %s", typeName)
}

func (r *Runner) bashPPSprint165MapLookup(meta *bashPPCollectionMeta, value any, valueMeta *bashPPCollectionMeta, typ syntax.BashPPTypeExpr) (string, *bashPPMapEntry, bool, error) {
	key, nonreflexive, err := r.bashPPSprint165MapKey(value, valueMeta, typ)
	if err != nil || nonreflexive || meta == nil {
		return "", nil, false, err
	}
	bashPPStorageMu.RLock()
	entry, found := meta.mapKeys[key]
	bashPPStorageMu.RUnlock()
	if !found {
		return "", nil, false, nil
	}
	return entry.storage, entry, true, nil
}

func (r *Runner) bashPPSprint165MapDelete(mapping map[string]any, meta *bashPPCollectionMeta, value any, valueMeta *bashPPCollectionMeta, typ syntax.BashPPTypeExpr) error {
	key, nonreflexive, err := r.bashPPSprint165MapKey(value, valueMeta, typ)
	if err != nil || nonreflexive || meta == nil {
		return err
	}
	bashPPStorageMu.Lock()
	defer bashPPStorageMu.Unlock()
	entry := meta.mapKeys[key]
	if entry == nil {
		return nil
	}
	delete(mapping, entry.storage)
	delete(meta.mapping, entry.storage)
	delete(meta.mapKeys, key)
	entry.dead = true
	if meta.mapOrderValid {
		meta.mapOrderDead++
	}
	return nil
}

func (r *Runner) bashPPSprint165MapStore(mapping map[string]any, meta *bashPPCollectionMeta, value any, valueMeta *bashPPCollectionMeta, typ syntax.BashPPTypeExpr, element any, child *bashPPCollectionMeta) (string, error) {
	if mapping == nil {
		if r.bashPPGoSource {
			r.goSourceRuntimePanic("assignment to entry in nil map")
			return "", errBashPPScalarInterrupted
		}
		return "", errBashPPNilMapAssign
	}
	key, nonreflexive, err := r.bashPPSprint165MapKey(value, valueMeta, typ)
	if err != nil {
		return "", err
	}
	bashPPStorageMu.Lock()
	defer bashPPStorageMu.Unlock()
	if meta.mapKeys == nil {
		meta.mapKeys = make(map[bashPPMapKey]*bashPPMapEntry)
	}
	if !nonreflexive {
		if entry := meta.mapKeys[key]; entry != nil {
			mapping[entry.storage], meta.mapping[entry.storage] = element, child
			return entry.storage, nil
		}
	} else {
		meta.mapNonce++
		key.nonce = meta.mapNonce
	}
	meta.mapNonce++
	storage := bashPPSprint165MapStorageName(value, valueMeta, meta.mapNonce)
	if _, exists := mapping[storage]; exists {
		for {
			meta.mapNonce++
			candidate := storage + "#map" + strconv.FormatUint(meta.mapNonce, 10)
			if _, exists := mapping[candidate]; !exists {
				storage = candidate
				break
			}
		}
	}
	entry := &bashPPMapEntry{storage: storage, key: value, keyMeta: valueMeta}
	meta.mapKeys[key] = entry
	meta.mapOrder, meta.mapOrderDead, meta.mapOrderValid = nil, 0, false
	mapping[storage], meta.mapping[storage] = element, child
	return storage, nil
}

func bashPPSprint165MapEntries(meta *bashPPCollectionMeta) []*bashPPMapEntry {
	if meta == nil {
		return nil
	}
	bashPPStorageMu.Lock()
	defer bashPPStorageMu.Unlock()
	if !meta.mapOrderValid {
		entries := make([]*bashPPMapEntry, 0, len(meta.mapKeys))
		for _, entry := range meta.mapKeys {
			entries = append(entries, entry)
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].storage < entries[j].storage })
		meta.mapOrder, meta.mapOrderDead, meta.mapOrderValid = entries, 0, true
		return entries
	}
	// Deleted entries only leave the cached order lazily: drop a dead prefix
	// (the drain-by-range idiom) and compact once they are the majority. A
	// dead entry still returned is skipped by callers, which re-check each
	// entry's storage before use exactly as for a snapshot.
	order := meta.mapOrder
	for len(order) > 0 && order[0].dead {
		order = order[1:]
		meta.mapOrderDead--
	}
	if meta.mapOrderDead > 0 && meta.mapOrderDead*2 > len(order) {
		live := make([]*bashPPMapEntry, 0, len(order)-meta.mapOrderDead)
		for _, entry := range order {
			if !entry.dead {
				live = append(live, entry)
			}
		}
		order, meta.mapOrderDead = live, 0
	}
	meta.mapOrder = order
	return order
}

func bashPPSprint165MapHasTypedKeys(meta *bashPPCollectionMeta) bool {
	if meta == nil {
		return false
	}
	bashPPStorageMu.RLock()
	typed := meta.mapKeys != nil
	bashPPStorageMu.RUnlock()
	return typed
}

func bashPPSprint165MapEntryTable(meta *bashPPCollectionMeta) map[bashPPMapKey]*bashPPMapEntry {
	if meta == nil {
		return nil
	}
	bashPPStorageMu.RLock()
	if meta.mapKeys == nil {
		bashPPStorageMu.RUnlock()
		return nil
	}
	entries := make(map[bashPPMapKey]*bashPPMapEntry, len(meta.mapKeys))
	for key, entry := range meta.mapKeys {
		entries[key] = entry
	}
	bashPPStorageMu.RUnlock()
	return entries
}

func bashPPSprint165MapEntryValue(mapping map[string]any, meta *bashPPCollectionMeta, storage string) (any, *bashPPCollectionMeta, bool) {
	bashPPStorageMu.RLock()
	value, found := mapping[storage]
	var child *bashPPCollectionMeta
	if meta != nil && meta.mapping != nil {
		child = meta.mapping[storage]
	}
	bashPPStorageMu.RUnlock()
	return value, child, found
}

func bashPPSprint165MapEntryValueFound(mapping map[string]any, meta *bashPPCollectionMeta, storage string, found bool) (any, *bashPPCollectionMeta) {
	if !found {
		return nil, nil
	}
	value, child, _ := bashPPSprint165MapEntryValue(mapping, meta, storage)
	return value, child
}

func bashPPSprint165MapClear(mapping map[string]any, meta *bashPPCollectionMeta) {
	bashPPStorageMu.Lock()
	clear(mapping)
	if meta != nil {
		clear(meta.mapping)
		for _, entry := range meta.mapKeys {
			entry.dead = true
		}
		clear(meta.mapKeys)
		meta.mapOrder, meta.mapOrderDead, meta.mapOrderValid = nil, 0, false
	}
	bashPPStorageMu.Unlock()
}
