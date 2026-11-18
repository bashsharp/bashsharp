// Copyright (c) 2026, the bash++ authors
// See LICENSE for licensing information

package expand

import (
	"encoding"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

const (
	invalidObjectString = "<invalid object>"
	maxObjectDepth      = 100
	maxObjectJSONBytes  = 64 << 20
	maxObjectVisits     = 1 << 20
)

var (
	jsonMarshalerType = reflect.TypeFor[json.Marshaler]()
	textMarshalerType = reflect.TypeFor[encoding.TextMarshaler]()
	stringerType      = reflect.TypeFor[fmt.Stringer]()
	errorType         = reflect.TypeFor[error]()
	jsonNumberType    = reflect.TypeFor[json.Number]()
)

type invalidObject struct{}

func (invalidObject) MarshalJSON() ([]byte, error) {
	return nil, fmt.Errorf("invalid object")
}

// Objects are the bash++ dialect's one value kind which is not a string. The
// shell itself remains string-typed: an [Object] only stays an object while it
// is held by a variable, and the moment it is used as a word — interpolated,
// compared, or passed as an argument — it is coerced by [ObjectString].
//
// The coercion is JSON. That single choice is what makes objects safe to add to
// a POSIX shell: every consumer that predates them, from `echo` to an external
// binary, keeps seeing a plain string, and the only thing that changes is that
// the string is now structured. Serializing on the way out is what the OS
// boundary does; see [github.com/bashsharp/bashsharp/engine/interp] and its exec environment.

// ObjectString renders val as the string the shell should see for it, which is
// its JSON encoding. Values which cannot be safely marshaled as JSON — channels,
// functions, cyclic structures, excessive depth, or values with marshaling,
// string, or error methods — return one fixed marker. The coercion stays total,
// but it never invokes caller-defined methods on invalid values.
//
// The encoding is deterministic: map keys are sorted by encoding/json, and
// struct fields keep their declaration order.
func ObjectString(val any) string {
	if val == nil {
		return "null"
	}
	state := newObjectPreflight()
	if err := preflightObject(reflect.ValueOf(val), 0, make(map[visit]bool), state); err != nil {
		return invalidObjectString
	}
	b, err := json.Marshal(val)
	if err != nil || len(b) > maxObjectJSONBytes {
		return invalidObjectString
	}
	return string(b)
}

// ValidObject reports whether val may be stored in an [Object] variable, which
// requires that it round-trips through JSON. Callers should use this to reject
// a bad value at the point of assignment, where an error can still be reported,
// rather than at expansion time where it cannot.
func ValidObject(val any) error {
	if val == nil {
		return nil // encodes as JSON null
	}
	state := newObjectPreflight()
	if err := preflightObject(reflect.ValueOf(val), 0, make(map[visit]bool), state); err != nil {
		return fmt.Errorf("value cannot be used as an object: %w", err)
	}
	b, err := json.Marshal(val)
	if err != nil {
		return fmt.Errorf("value cannot be used as an object: %w", err)
	}
	if len(b) > maxObjectJSONBytes {
		return fmt.Errorf("value cannot be used as an object: JSON encoding exceeds %d bytes", maxObjectJSONBytes)
	}
	return nil
}

// NewObject builds an [Object] variable holding val.
func NewObject(val any) Variable {
	if err := ValidObject(val); err != nil {
		val = invalidObject{}
	}
	return Variable{Set: true, Kind: Object, Obj: val}
}

type visit struct {
	typ reflect.Type
	ptr uintptr
}

type objectPreflight struct {
	visits       int
	encodedBytes int64
}

func newObjectPreflight() *objectPreflight {
	return &objectPreflight{}
}

func (p *objectPreflight) addBytes(n int64) error {
	if n < 0 || p.encodedBytes > int64(maxObjectJSONBytes)-n {
		return fmt.Errorf("object JSON encoding exceeds %d bytes", maxObjectJSONBytes)
	}
	p.encodedBytes += n
	return nil
}

func preflightObject(v reflect.Value, depth int, seen map[visit]bool, state *objectPreflight) error {
	state.visits++
	if state.visits > maxObjectVisits {
		return fmt.Errorf("object graph exceeds maximum traversal work %d", maxObjectVisits)
	}
	if depth > maxObjectDepth {
		return fmt.Errorf("object graph exceeds maximum depth %d", maxObjectDepth)
	}
	if !v.IsValid() {
		return nil
	}
	if typeHasCallerMethod(v.Type()) {
		return fmt.Errorf("reachable type %v has caller-defined coercion method", v.Type())
	}
	switch v.Kind() {
	case reflect.Interface, reflect.Pointer:
		if v.IsNil() {
			return state.addBytes(4) // null
		}
		if v.Kind() == reflect.Pointer {
			vis := visit{typ: v.Type(), ptr: v.Pointer()}
			if seen[vis] {
				return fmt.Errorf("object graph contains a cycle")
			}
			seen[vis] = true
			defer delete(seen, vis)
		}
		return preflightObject(v.Elem(), depth+1, seen, state)
	case reflect.Bool:
		return state.addBytes(5) // false is the longer spelling
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return state.addBytes(int64(len(strconv.FormatInt(v.Int(), 10))))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return state.addBytes(int64(len(strconv.FormatUint(v.Uint(), 10))))
	case reflect.String:
		return state.addBytes(jsonStringSize(v.String()))
	case reflect.Float32, reflect.Float64:
		f := v.Float()
		if math.IsInf(f, 0) || math.IsNaN(f) {
			return fmt.Errorf("non-finite float cannot be used as a JSON object value")
		}
		// The longest finite float spelling emitted by encoding/json is well
		// below this bound, including exponent and sign.
		return state.addBytes(32)
	case reflect.Array:
		if err := state.addBytes(collectionOverhead(v.Len())); err != nil {
			return err
		}
		for i := 0; i < v.Len(); i++ {
			if err := preflightObject(v.Index(i), depth+1, seen, state); err != nil {
				return err
			}
		}
		return nil
	case reflect.Slice:
		if v.IsNil() {
			return state.addBytes(4) // null
		}
		vis := visit{typ: v.Type(), ptr: v.Pointer()}
		if seen[vis] {
			return fmt.Errorf("object graph contains a cycle")
		}
		seen[vis] = true
		defer delete(seen, vis)
		if isJSONByteSlice(v.Type()) {
			// encoding/json uses a quoted base64 string for ordinary byte
			// slices. Account for that expansion without walking or encoding
			// the entire slice.
			return state.addBytes(2 + 4*((int64(v.Len())+2)/3))
		}
		if err := state.addBytes(collectionOverhead(v.Len())); err != nil {
			return err
		}
		for i := 0; i < v.Len(); i++ {
			if err := preflightObject(v.Index(i), depth+1, seen, state); err != nil {
				return err
			}
		}
		return nil
	case reflect.Map:
		if v.IsNil() {
			return state.addBytes(4) // null
		}
		if !validJSONMapKey(v.Type().Key()) {
			return fmt.Errorf("map key type %v is not supported by JSON objects", v.Type().Key())
		}
		// Maps do not expose Pointer, but UnsafePointer is the supported
		// reflect identity for map values. It is only used for cycle detection;
		// the original value is still passed to json.Marshal.
		vis := visit{typ: v.Type(), ptr: uintptr(v.UnsafePointer())}
		if seen[vis] {
			return fmt.Errorf("object graph contains a cycle")
		}
		seen[vis] = true
		defer delete(seen, vis)
		// Braces, commas, and one colon per entry. Map keys are JSON strings;
		// preflightObject accounts for their scalar spelling, and the extra two
		// bytes below safely cover quotes for integer keys (and overbound string
		// keys by two bytes).
		if err := state.addBytes(collectionOverhead(v.Len()) + 3*int64(v.Len())); err != nil {
			return err
		}
		iter := v.MapRange()
		for iter.Next() {
			if err := preflightObject(iter.Key(), depth+1, seen, state); err != nil {
				return err
			}
			if err := preflightObject(iter.Value(), depth+1, seen, state); err != nil {
				return err
			}
		}
		return nil
	case reflect.Struct:
		if err := state.addBytes(2); err != nil { // braces
			return err
		}
		for n, field := range objectStructPlan(v.Type()) {
			if field.isZero != nil {
				return fmt.Errorf("reachable type %v has caller-defined IsZero method used by omitzero", field.isZero)
			}
			if n > 0 {
				if err := state.addBytes(1); err != nil { // comma
					return err
				}
			}
			if err := state.addBytes(field.nameBytes + 1); err != nil { // field name and colon
				return err
			}
			if err := preflightObject(v.Field(field.index), depth+1, seen, state); err != nil {
				return err
			}
			if field.quoted {
				if err := state.addBytes(quotedFieldExtra(v.Field(field.index))); err != nil {
					return err
				}
			}
		}
		return nil
	default:
		return fmt.Errorf("unsupported JSON object kind %v", v.Kind())
	}
}

func collectionOverhead(length int) int64 {
	if length == 0 {
		return 2
	}
	return int64(length) + 1 // delimiters and length-1 commas
}

// objectStructField is what the preflight needs of one reachable struct
// field: where it is, how many bytes its name can encode to, and whether its
// tag quotes the value. isZero is the field's type when omitzero would call a
// caller-defined IsZero method, which refuses the whole value.
type objectStructField struct {
	index     int
	nameBytes int64
	quoted    bool
	isZero    reflect.Type
}

// objectStructPlans memoizes objectStructPlan per struct type. The plan is
// read from the type's field tags alone, and the preflight walks it for every
// struct of an object graph, so a value returned in a loop would otherwise
// parse the same tags again on every iteration.
var objectStructPlans sync.Map // reflect.Type -> []objectStructField

func objectStructPlan(t reflect.Type) []objectStructField {
	if plan, ok := objectStructPlans.Load(t); ok {
		return plan.([]objectStructField)
	}
	var plan []objectStructField
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		fieldInfo, reachable := objectJSONFieldInfo(field)
		if !reachable {
			continue
		}
		planned := objectStructField{index: i, quoted: fieldInfo.quoted}
		if fieldInfo.omitZero && typeHasIsZeroMethod(field.Type) {
			planned.isZero = field.Type
		}
		planned.nameBytes = jsonStringSize(fieldInfo.name)
		if fallback := jsonStringSize(field.Name); fallback > planned.nameBytes {
			planned.nameBytes = fallback
		}
		plan = append(plan, planned)
	}
	objectStructPlans.Store(t, plan)
	return plan
}

type objectJSONField struct {
	name     string
	quoted   bool
	omitZero bool
}

func objectJSONFieldInfo(field reflect.StructField) (objectJSONField, bool) {
	tag := field.Tag.Get("json")
	if tag == "-" {
		return objectJSONField{}, false
	}
	if !field.IsExported() && !field.Anonymous {
		return objectJSONField{}, false
	}
	name, options, _ := strings.Cut(tag, ",")
	if name == "" {
		name = field.Name
	}
	info := objectJSONField{name: name}
	for options != "" {
		var option string
		option, options, _ = strings.Cut(options, ",")
		switch option {
		case "omitzero":
			info.omitZero = true
		case "string":
			ft := field.Type
			if ft.Name() == "" && ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			switch ft.Kind() {
			case reflect.Bool,
				reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
				reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
				reflect.Float32, reflect.Float64, reflect.String:
				info.quoted = true
			}
		}
	}
	return info, true
}

func jsonStringSize(s string) int64 {
	size, _ := jsonStringMetrics(s)
	return size
}

func jsonStringMetrics(s string) (size, backslashes int64) {
	size = 2 // quotes
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			i++
			switch c {
			case '\\', '"', '\b', '\f', '\n', '\r', '\t':
				size += 2
				backslashes++
			case '<', '>', '&':
				size += 6 // encoding/json's default HTML escaping
				backslashes++
			default:
				if c < 0x20 {
					size += 6
					backslashes++
				} else {
					size++
				}
			}
			continue
		}
		r, width := utf8.DecodeRuneInString(s[i:])
		i += width
		if r == utf8.RuneError && width == 1 {
			size += 6 // \\ufffd
			backslashes++
		} else if r == '\u2028' || r == '\u2029' {
			size += 6
			backslashes++
		} else {
			size += int64(width)
		}
	}
	return size, backslashes
}

func quotedFieldExtra(v reflect.Value) int64 {
	for v.IsValid() && (v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface) {
		if v.IsNil() {
			return 0 // nil pointers encode as null even with ,string
		}
		v = v.Elem()
	}
	if !v.IsValid() {
		return 0
	}
	if v.Kind() == reflect.String && v.Type() != jsonNumberType {
		_, backslashes := jsonStringMetrics(v.String())
		// The outer quotes add two bytes, the inner quotes must be escaped
		// (two more), and each existing escape gains another backslash.
		return 4 + backslashes
	}
	return 2 // quotes around booleans and numbers
}

func typeHasIsZeroMethod(t reflect.Type) bool {
	isZeroerType := reflect.TypeFor[interface{ IsZero() bool }]()
	return t.Implements(isZeroerType) ||
		(t.Kind() != reflect.Pointer && reflect.PointerTo(t).Implements(isZeroerType))
}

func isJSONByteSlice(t reflect.Type) bool {
	if t.Kind() != reflect.Slice || t.Elem().Kind() != reflect.Uint8 {
		return false
	}
	ptrElem := reflect.PointerTo(t.Elem())
	return !ptrElem.Implements(jsonMarshalerType) && !ptrElem.Implements(textMarshalerType)
}

// callerMethodTypes memoizes typeHasCallerMethod per type. The answer is a
// property of the type alone, and the preflight asks it for every node of an
// object graph, so an interpreter appending to a long list would otherwise
// repeat the same eight interface checks per element on every append.
var callerMethodTypes sync.Map // reflect.Type -> bool

func typeHasCallerMethod(t reflect.Type) bool {
	if has, ok := callerMethodTypes.Load(t); ok {
		return has.(bool)
	}
	has := computeTypeHasCallerMethod(t)
	callerMethodTypes.Store(t, has)
	return has
}

func computeTypeHasCallerMethod(t reflect.Type) bool {
	if t == jsonNumberType {
		// The JSON decoder uses json.Number to preserve source spelling.
		// encoding/json handles it as a scalar without calling String.
		return false
	}
	if implementsUnsafeObjectMethod(t) {
		return true
	}
	if t.Kind() != reflect.Pointer && implementsUnsafeObjectMethod(reflect.PointerTo(t)) {
		return true
	}
	return false
}

func implementsUnsafeObjectMethod(t reflect.Type) bool {
	return t.Implements(jsonMarshalerType) ||
		t.Implements(textMarshalerType) ||
		t.Implements(stringerType) ||
		t.Implements(errorType)
}

func validJSONMapKey(t reflect.Type) bool {
	if typeHasCallerMethod(t) {
		return false
	}
	switch t.Kind() {
	case reflect.String,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return true
	default:
		return false
	}
}

// ObjectsEnabled reports whether object-valued variables are available, which
// is true only under the bash++ dialect ([syntax.LangBashPP]). Every other
// variant, including the zero value [syntax.LangBash], returns false.
//
// This is the single source of truth for the gate. The interpreter consults it
// on the assignment path and refuses to create an [Object] when it is false, so
// that under any other dialect an object simply cannot come into existence and
// the rest of expansion never has to account for one.
func (cfg *Config) ObjectsEnabled() bool {
	return cfg.Lang == syntax.LangBashPP
}
