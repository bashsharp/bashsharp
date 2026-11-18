package shellrt

import (
	"fmt"
	"reflect"
)

// AssignReadonlyPointer receives an already checked pointer and the converted
// RHS, preserving pointer evaluation, RHS evaluation, guard, and store order.
func AssignReadonlyPointer[T any](state *ReadonlyState, pointer *T, value T) {
	state.mu.RLock()
	owner := state.resolveOwner(pointer, pointer)
	state.mu.RUnlock()
	if owner != "" {
		kind := reflect.TypeFor[T]().Kind()
		if kind == reflect.Struct || kind == reflect.Array || kind == reflect.Map || kind == reflect.Slice {
			panic(&ReadonlyError{Message: fmt.Sprintf("cannot mutate readonly value %q through pointer", owner)})
		}
		panic(&ReadonlyError{Message: "cannot mutate readonly value through pointer"})
	}
	*pointer = value
}
