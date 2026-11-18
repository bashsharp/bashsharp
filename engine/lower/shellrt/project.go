// Copyright (c) 2026, the bash++ authors
// See LICENSE for licensing information

package shellrt

import (
	"encoding"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
)

// This file is linked into every generated program, so it stays on the
// standard library. It is a faithful port of the interpreter's object coercion
// in expand/object.go, not an independent design; the two are held together by
// TestProjectMatchesInterpreterCoercion, which diffs this package's output
// against expand.ObjectString over a shared table. The dependency direction is
// test-only on purpose: the generated program must not pull in expand's
// transitive module graph.

// Kind is the projection the compiler selected for a value. The compiler
// decides it from the binding's declared provenance, never from the value's
// runtime shape: an imported package call's result is KindObject even when it
// happens to be a string, and a field selected out of a rich value is
// KindScalar even when the field itself is a plain scalar.
//
// Passing the kind explicitly is what keeps a plain shell string from being
// relabelled as JSON because of what it happens to contain.
type Kind int

const (
	// KindScalar renders a native scalar as the plain shell text the
	// interpreter produces for it. No quoting, no JSON. It is reflect-based
	// so a named type such as `type Count int` projects plain like its
	// underlying kind; the interpreter renders `var c Count = 7` as 7.
	KindScalar Kind = iota
	// KindObject renders a rich root through the shell's object coercion,
	// which is JSON. A nil map or nil slice renders "null".
	KindObject
	// KindPointer renders a pointer root. The interpreter projects every
	// pointer root as the empty string, nil or not: &S{}, &namedInt, new(S)
	// and a nil *S all interpolate empty.
	KindPointer
	// KindInterface renders an interface root: empty when the interface is
	// nil, and otherwise the plain scalar text of the named value it holds.
	KindInterface
	// KindFloat renders a floating binding whose value crossed a foreign
	// boundary. The interpreter renders a foreign float result with
	// strconv.FormatFloat 'g' (interp's foreignResult), so this kind carries
	// runtime provenance; native floats without retained literal provenance
	// remain refused under KindScalar.
	KindFloat
	// KindForeignBytes preserves the raw shell text of a foreign bytes result.
	KindForeignBytes
)

// InvalidObject is the one fixed marker every unsafely coercible value
// collapses to. It is not an invention of this package: it matches the
// interpreter's own marker in expand/object.go byte for byte.
const InvalidObject = "<invalid object>"

const (
	maxObjectDepth     = 100
	maxObjectJSONBytes = 64 << 20
	maxObjectVisits    = 1 << 20
)

var (
	jsonMarshalerType = reflect.TypeFor[json.Marshaler]()
	textMarshalerType = reflect.TypeFor[encoding.TextMarshaler]()
	stringerType      = reflect.TypeFor[fmt.Stringer]()
	jsonNumberType    = reflect.TypeFor[json.Number]()
	isZeroerType      = reflect.TypeFor[interface{ IsZero() bool }]()
	projectErrorType  = reflect.TypeFor[projectError]()
)

// ProjectionError is the typed failure a projection raises. It is a distinct
// type so a program's failure plumbing can recognise it while unwinding,
// rather than having to match on message text.
type ProjectionError struct {
	Kind Kind
	Msg  string
}

func (e *ProjectionError) Error() string { return "shellrt: " + e.Msg }

// AsProjectionError reports whether a recovered value is a projection failure,
// for the generated program's own failure plumbing.
func AsProjectionError(recovered any) (*ProjectionError, bool) {
	err, ok := recovered.(*ProjectionError)
	return err, ok
}

type projectError interface {
	shellrtProjectError() string
}

type trustedErrorText string

func (e trustedErrorText) shellrtProjectError() string { return string(e) }
func (e trustedErrorText) Error() string               { return string(e) }

// TrustedErrorText adapts an already-sanitized error string for projection
// without giving the projected value a caller-controlled Error method. It is
// the only error-like dynamic value interfaceText trusts.
func TrustedErrorText(message string) error { return trustedErrorText(message) }

// ProjectErr renders value as the shell text for the given kind, returning any
// failure. It is pure: it reads value, touches no package-level state, and in
// particular never writes Status. Callers that own their own failure plumbing
// should use this and not Project.
func ProjectErr(value any, kind Kind) (string, error) {
	switch kind {
	case KindObject:
		return objectText(value), nil
	case KindPointer:
		// Measured: every pointer root interpolates empty, nil or not.
		return "", nil
	case KindInterface:
		return interfaceText(value)
	case KindForeignBytes:
		if data, ok := value.([]byte); ok {
			return ForeignBytesProjection(data), nil
		}
		return "", &ProjectionError{Kind: kind, Msg: "foreign bytes projection requires []byte"}
	case KindFloat:
		return floatText(value)
	default:
		return scalarText(value, kind)
	}
}

// floatText renders a floating binding that carries runtime provenance — a
// foreign call result, which the interpreter renders with FormatFloat 'g'
// rather than from a retained source literal. A non-float value under this
// kind falls back to the scalar rules.
func floatText(value any) (string, error) {
	v := reflect.ValueOf(value)
	if v.IsValid() {
		switch v.Kind() {
		case reflect.Float32, reflect.Float64:
			return strconv.FormatFloat(v.Float(), 'g', -1, 64), nil
		}
	}
	return scalarText(value, KindFloat)
}

// Project is ProjectErr for call sites that have no failure plumbing of their
// own. A failure raises the *ProjectionError rather than reporting it, because
// reporting it cannot work here: Echo and Printf both begin by assigning
// Status = 0, so the very next operation in a statement such as
// Echo(Project(bad, KindScalar)) would clear the failure and the program would
// continue through the rest of its body as though the projection had succeeded.
// Only an unwind survives that. The generated program's panic boundary turns it
// into a nonzero exit with the message on stderr.
func Project(value any, kind Kind) string {
	text, err := ProjectErr(value, kind)
	if err != nil {
		if projectionErr, ok := err.(*ProjectionError); ok {
			panic(projectionErr)
		}
		panic(&ProjectionError{Kind: kind, Msg: err.Error()})
	}
	return text
}

// interfaceText renders an interface root. Unlike every other kind, this one
// legitimately dispatches on the dynamic value: an interface's rendering is a
// runtime fact that no compiler metadata can settle, and the interpreter
// dispatches the same way. Measured — a nil interface is empty, an interface
// holding a named scalar renders plain (i=[7]), and one holding a named
// struct, map or slice renders JSON ({"N":3}, {"a":1}, [1,2]). A pointer held
// in an interface is empty, nil or not, like every other pointer root.
func interfaceText(value any) (string, error) {
	v := reflect.ValueOf(value)
	if !v.IsValid() || isNilValue(v) {
		return "", nil
	}
	if err, ok := value.(projectError); ok {
		return err.shellrtProjectError(), nil
	}
	switch v.Kind() {
	case reflect.Pointer:
		return "", nil
	case reflect.Struct, reflect.Map, reflect.Slice, reflect.Array:
		return objectText(value), nil
	default:
		return scalarText(value, KindInterface)
	}
}

// isNilValue reports a nil held inside an interface without calling anything on
// the value.
func isNilValue(v reflect.Value) bool {
	if !v.IsValid() {
		return true
	}
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		return v.IsNil()
	}
	return false
}

func objectText(value any) string {
	if value == nil {
		return "null"
	}
	if err := preflight(reflect.ValueOf(value), 0, map[visit]bool{}, new(int)); err != nil {
		return InvalidObject
	}
	b, err := json.Marshal(value)
	if err != nil || len(b) > maxObjectJSONBytes {
		return InvalidObject
	}
	return string(b)
}

type visit struct {
	typ reflect.Type
	ptr uintptr
}

// preflight refuses everything the interpreter refuses, before any marshaling
// begins. Refusing first is what keeps a caller-defined method from running and
// keeps a capability from reaching the encoder at all.
func preflight(v reflect.Value, depth int, seen map[visit]bool, visits *int) error {
	*visits++
	if *visits > maxObjectVisits {
		return fmt.Errorf("object graph exceeds maximum traversal work")
	}
	if depth > maxObjectDepth {
		return fmt.Errorf("object graph exceeds maximum depth")
	}
	if !v.IsValid() {
		return nil
	}
	if hasCallerMethod(v.Type()) {
		return fmt.Errorf("reachable type %v has caller-defined coercion method", v.Type())
	}
	switch v.Kind() {
	case reflect.Interface, reflect.Pointer:
		if v.IsNil() {
			return nil
		}
		if v.Kind() == reflect.Pointer {
			vis := visit{typ: v.Type(), ptr: v.Pointer()}
			if seen[vis] {
				return fmt.Errorf("object graph contains a cycle")
			}
			seen[vis] = true
			defer delete(seen, vis)
		}
		return preflight(v.Elem(), depth+1, seen, visits)
	case reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.String:
		return nil
	case reflect.Float32, reflect.Float64:
		if f := v.Float(); math.IsInf(f, 0) || math.IsNaN(f) {
			return fmt.Errorf("non-finite float cannot be used as a JSON object value")
		}
		return nil
	case reflect.Array:
		for i := 0; i < v.Len(); i++ {
			if err := preflight(v.Index(i), depth+1, seen, visits); err != nil {
				return err
			}
		}
		return nil
	case reflect.Slice:
		if v.IsNil() {
			return nil
		}
		vis := visit{typ: v.Type(), ptr: v.Pointer()}
		if seen[vis] {
			return fmt.Errorf("object graph contains a cycle")
		}
		seen[vis] = true
		defer delete(seen, vis)
		if isJSONByteSlice(v.Type()) {
			// encoding/json emits a base64 string; there is nothing to walk.
			return nil
		}
		for i := 0; i < v.Len(); i++ {
			if err := preflight(v.Index(i), depth+1, seen, visits); err != nil {
				return err
			}
		}
		return nil
	case reflect.Map:
		if v.IsNil() {
			return nil
		}
		if !validJSONMapKey(v.Type().Key()) {
			return fmt.Errorf("map key type %v is not supported by JSON objects", v.Type().Key())
		}
		// Maps do not expose Pointer; UnsafePointer is the supported reflect
		// identity. It is used only for cycle detection.
		vis := visit{typ: v.Type(), ptr: uintptr(v.UnsafePointer())}
		if seen[vis] {
			return fmt.Errorf("object graph contains a cycle")
		}
		seen[vis] = true
		defer delete(seen, vis)
		iter := v.MapRange()
		for iter.Next() {
			if err := preflight(iter.Key(), depth+1, seen, visits); err != nil {
				return err
			}
			if err := preflight(iter.Value(), depth+1, seen, visits); err != nil {
				return err
			}
		}
		return nil
	case reflect.Struct:
		t := v.Type()
		for i := 0; i < v.NumField(); i++ {
			field := t.Field(i)
			omitZero, reachable := fieldInfo(field)
			if !reachable {
				continue
			}
			if omitZero && hasIsZeroMethod(field.Type) {
				return fmt.Errorf("reachable type %v has caller-defined IsZero method used by omitzero", field.Type)
			}
			if err := preflight(v.Field(i), depth+1, seen, visits); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("unsupported JSON object kind %v", v.Kind())
	}
}

// fieldInfo reports whether a struct field is reachable by encoding/json and
// whether it carries the omitzero option.
func fieldInfo(field reflect.StructField) (omitZero, reachable bool) {
	tag := field.Tag.Get("json")
	if tag == "-" {
		return false, false
	}
	if !field.IsExported() && !field.Anonymous {
		return false, false
	}
	_, options, _ := strings.Cut(tag, ",")
	for options != "" {
		var option string
		option, options, _ = strings.Cut(options, ",")
		if option == "omitzero" {
			omitZero = true
		}
	}
	return omitZero, true
}

func hasIsZeroMethod(t reflect.Type) bool {
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

func hasCallerMethod(t reflect.Type) bool {
	if t == jsonNumberType {
		// encoding/json handles json.Number as a scalar without calling String.
		return false
	}
	if implementsUnsafeMethod(t) {
		return true
	}
	return t.Kind() != reflect.Pointer && implementsUnsafeMethod(reflect.PointerTo(t))
}

func implementsUnsafeMethod(t reflect.Type) bool {
	return t.Implements(jsonMarshalerType) ||
		t.Implements(textMarshalerType) ||
		t.Implements(stringerType) ||
		t.Implements(projectErrorType)
}

func validJSONMapKey(t reflect.Type) bool {
	if hasCallerMethod(t) {
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

// scalarText renders a native scalar as plain interpreter text. It uses
// reflect kinds rather than a concrete type switch so that a named type
// projects like its underlying kind — the interpreter renders `var c Count = 7`
// as 7 and `var n Name = "hi"` as hi. Only the kind is read; no method on the
// value is consulted or invoked, so a named type carrying String or Error
// cannot execute caller code here.
//
// Floats are refused. Measured against the engine, the only float path that
// renders at all is an untyped literal at its binding site, which the compiler
// projects from retained provenance without reaching the runtime: 1.5 is the
// exact rational 3/2 and 1.1 is 11/10. A float64 in hand no longer carries that
// provenance, so producing a decimal here would print something the interpreter
// never prints.
func scalarText(value any, kind Kind) (string, error) {
	v := reflect.ValueOf(value)
	if !v.IsValid() {
		return "", nil
	}
	switch v.Kind() {
	case reflect.String:
		return v.String(), nil
	case reflect.Bool:
		return strconv.FormatBool(v.Bool()), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(v.Int(), 10), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return strconv.FormatUint(v.Uint(), 10), nil
	case reflect.Float32, reflect.Float64:
		return "", &ProjectionError{Kind: kind, Msg: "floating scalar has no retained constant provenance; project it from its source literal"}
	default:
		return "", &ProjectionError{Kind: kind, Msg: fmt.Sprintf("%v is not a scalar projection", v.Kind())}
	}
}
