package shellrt

import (
	"fmt"
	"reflect"
)

// TupleValue retains a result's static Go type even when its interface payload
// is nil or has a more specific dynamic type.
type TupleValue struct {
	value any
	typ   reflect.Type
}

func TupleResult[T any](value T) TupleValue {
	return TupleValue{value: value, typ: reflect.TypeFor[T]()}
}

type TupleError struct {
	Message string
	Site    ValueSite
}

func (e *TupleError) Error() string {
	prefix := ""
	if e.Site.File != "" {
		prefix = fmt.Sprintf("%s: line %d: ", e.Site.File, e.Site.Line)
	}
	return prefix + "BASHPP-EASSIGN-TYPE: " + e.Message
}
func (*TupleError) ExitStatus() int { return 2 }

// AssignTuple validates the whole assignment before changing any binding.
// A nil target denotes the blank identifier. Callers evaluate the producer
// once before creating these result descriptors.
func AssignTuple(targets []any, values []TupleValue, site ValueSite) error {
	if len(targets) != len(values) {
		return &TupleError{Message: "tuple arity mismatch", Site: site}
	}
	slots := make([]reflect.Value, len(targets))
	for i, target := range targets {
		if target == nil {
			continue
		}
		pointer := reflect.ValueOf(target)
		if pointer.Kind() != reflect.Pointer || pointer.IsNil() {
			return &TupleError{Message: "assignment target is not a binding", Site: site}
		}
		slot := pointer.Elem()
		slots[i] = slot
		if !values[i].typ.AssignableTo(slot.Type()) {
			return &TupleError{Message: fmt.Sprintf("cannot assign %s to %s", valueTypeName(values[i].typ), valueTypeName(slot.Type())), Site: site}
		}
	}
	for i, slot := range slots {
		if !slot.IsValid() {
			continue
		}
		if values[i].value == nil {
			slot.SetZero()
		} else {
			slot.Set(reflect.ValueOf(values[i].value))
		}
	}
	return nil
}
