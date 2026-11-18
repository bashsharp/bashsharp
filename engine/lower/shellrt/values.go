package shellrt

import (
	"errors"
	"fmt"
	"reflect"
)

// ValueSite retains source identity without changing the engine's public text.
type ValueSite struct {
	File, Name           string
	Line, Column, Offset int
}

// ValueError is a positioned engine diagnostic. Reporting belongs to Program.
type ValueError struct {
	Code, Message string
	Site          ValueSite
}

func (e *ValueError) Error() string { return e.Code + ": " + e.Message }
func (*ValueError) ExitStatus() int { return 2 }

// ValueAbort unwinds generated expressions to their owning Program.Run boundary.
// It is not a language panic and must not be consumed by a user recover call.
type ValueAbort struct{ Err error }

func (a ValueAbort) Error() string { return a.Err.Error() }
func (a ValueAbort) Unwrap() error { return a.Err }
func (ValueAbort) ExitStatus() int { return 2 }

func AsValueError(recovered any) (*ValueError, bool) {
	err, ok := recovered.(error)
	if !ok {
		return nil, false
	}
	var value *ValueError
	ok = errors.As(err, &value)
	return value, ok
}
func MustValue[T any](value T, err error) T {
	if err != nil {
		panic(ValueAbort{Err: err})
	}
	return value
}
func MustAssertOK[T any](value T, ok bool, err error) (T, bool) {
	if err != nil {
		panic(ValueAbort{Err: err})
	}
	return value, ok
}

func CheckedPointer[T any](pointer *T, site ValueSite) (*T, error) {
	if pointer == nil {
		return nil, &ValueError{Code: "BASHPP-ENIL-DEREF", Message: "dereference of nil pointer", Site: site}
	}
	return pointer, nil
}
func Deref[T any](pointer *T, site ValueSite) (T, error) {
	checked, err := CheckedPointer(pointer, site)
	if err != nil {
		var zero T
		return zero, err
	}
	return *checked, nil
}

// Assertion supplies source type spellings for diagnostics only. Impossible is
// a compiler-proven classification, never guessed from the runtime value.
type Assertion struct {
	Source, Target, Dynamic string
	Impossible              bool
	Site                    ValueSite
}

func (a Assertion) impossibleError() error {
	if !a.Impossible {
		return nil
	}
	return &ValueError{Code: "BASHPP-EASSERT-IMPOSSIBLE", Message: fmt.Sprintf("%s cannot be asserted from %s", a.Target, a.Source), Site: a.Site}
}
func AssertOK[T any](value any, assertion Assertion) (T, bool, error) {
	if err := assertion.impossibleError(); err != nil {
		var zero T
		return zero, false, err
	}
	result, ok := value.(T)
	return result, ok, nil
}
func Assert[T any](value any, assertion Assertion) (T, error) {
	result, ok, err := AssertOK[T](value, assertion)
	if err != nil || ok {
		return result, err
	}
	dynamic := assertion.Dynamic
	if dynamic == "" {
		dynamic = valueTypeName(reflect.TypeOf(value))
	}
	target := assertion.Target
	if target == "" {
		target = valueTypeName(reflect.TypeFor[T]())
	}
	return result, &ValueError{Code: "BASHPP-EASSERT-FAIL", Message: fmt.Sprintf("interface value has dynamic type %s, not %s", dynamic, target), Site: assertion.Site}
}
func valueTypeName(typ reflect.Type) string {
	if typ == nil {
		return ""
	}
	if typ.Name() != "" {
		return typ.Name()
	}
	switch typ.Kind() {
	case reflect.Pointer:
		return "*" + valueTypeName(typ.Elem())
	case reflect.Slice:
		return "[]" + valueTypeName(typ.Elem())
	case reflect.Array:
		return fmt.Sprintf("[%d]%s", typ.Len(), valueTypeName(typ.Elem()))
	case reflect.Map:
		return "map[" + valueTypeName(typ.Key()) + "]" + valueTypeName(typ.Elem())
	}
	return typ.String()
}

// Integer admits every native index width without converting a uint64 through int.
type Integer interface {
	~int | ~int8 | ~int16 | ~int32 | ~int64 | ~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 | ~uintptr
}

func CheckIndex[I Integer](index I, length int, site ValueSite) (I, error) {
	if index < 0 || length < 0 || uint64(index) >= uint64(length) {
		return index, &ValueError{Code: "BASHPP-ECOLLECTION-BOUNDS", Message: fmt.Sprintf("index %d out of bounds for length %d", index, length), Site: site}
	}
	return index, nil
}
func Index[T any, S ~[]T, I Integer](sequence S, index I, site ValueSite) (T, error) {
	checked, err := CheckIndex(index, len(sequence), site)
	if err != nil {
		var zero T
		return zero, err
	}
	return sequence[checked], nil
}

// MakeSliceLength evaluates the source length once when capacity is omitted.
func MakeSliceLength[T any](length int, site ValueSite) ([]T, error) {
	return MakeSlice[T](length, length, site)
}
func MakeSlice[T any](length, capacity int, site ValueSite) ([]T, error) {
	if length < 0 || capacity < length {
		return nil, &ValueError{Code: "BASHPP-EBUILTIN-SIZE", Message: fmt.Sprintf("make slice length/capacity is invalid: %d/%d", length, capacity), Site: site}
	}
	return make([]T, length, capacity), nil
}
func MakeMap[K comparable, V any](size int, site ValueSite) (map[K]V, error) {
	if size < 0 {
		return nil, &ValueError{Code: "BASHPP-EBUILTIN-SIZE", Message: "make map size must not be negative", Site: site}
	}
	return make(map[K]V, size), nil
}
