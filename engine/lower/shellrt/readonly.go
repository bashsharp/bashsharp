package shellrt

import (
	"fmt"
	"reflect"
	"sync"
)

// ReadonlyState tracks runtime locations, not source variable spellings. Mark
// receives the address of a binding so root rebinding and deep object mutation
// remain distinct operations. Generated code must check the actual mutation
// container after evaluating its address/index operands exactly once.
type ReadonlyState struct {
	mu       sync.RWMutex
	roots    map[readonlyKey]string
	objects  map[readonlyKey]string
	slices   []readonlySlice
	retained []any
}
type readonlyKey struct {
	typ     reflect.Type
	address uintptr
}
type readonlySlice struct {
	typ          reflect.Type
	first, limit uintptr
	owner        string
}

type ReadonlyError struct{ Message string }

func (e *ReadonlyError) Error() string { return "BASHPP-EREADONLY-MUTATION: " + e.Message }
func (*ReadonlyError) ExitStatus() int { return 2 }

func readonlyObjectKey(v reflect.Value) (readonlyKey, bool) {
	if !v.IsValid() {
		return readonlyKey{}, false
	}
	switch v.Kind() {
	case reflect.Pointer, reflect.Map:
		if !v.IsNil() {
			return readonlyKey{v.Type(), v.Pointer()}, true
		}
	}
	return readonlyKey{}, false
}
func (s *ReadonlyState) Mark(name string, binding any) error {
	root := reflect.ValueOf(binding)
	if root.Kind() != reflect.Pointer || root.IsNil() {
		return fmt.Errorf("readonly binding must be a non-nil pointer")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.roots == nil {
		s.roots = map[readonlyKey]string{}
		s.objects = map[readonlyKey]string{}
	}
	key, _ := readonlyObjectKey(root)
	if _, exists := s.roots[key]; !exists {
		s.roots[key] = name
	}
	if _, exists := s.objects[key]; !exists {
		s.objects[key] = name
	}
	// Retention makes identity keys stable even after other aliases are rebound.
	s.retained = append(s.retained, binding)
	visited := map[readonlyKey]bool{}
	var visit func(reflect.Value)
	visit = func(v reflect.Value) {
		if !v.IsValid() {
			return
		}
		if v.CanAddr() {
			if key, ok := readonlyObjectKey(v.Addr()); ok {
				if _, exists := s.objects[key]; !exists {
					s.objects[key] = name
				}
			}
		}
		if v.Kind() == reflect.Interface {
			if !v.IsNil() {
				visit(v.Elem())
			}
			return
		}
		if k, ok := readonlyObjectKey(v); ok {
			if visited[k] {
				return
			}
			visited[k] = true
			if _, exists := s.objects[k]; !exists {
				s.objects[k] = name
			}
		}
		switch v.Kind() {
		case reflect.Pointer:
			if !v.IsNil() {
				visit(v.Elem())
			}
		case reflect.Map:
			iter := v.MapRange()
			for iter.Next() {
				visit(iter.Key())
				visit(iter.Value())
			}
		case reflect.Slice:
			sliceKey := readonlyKey{v.Type(), v.Pointer()}
			if visited[sliceKey] {
				return
			}
			visited[sliceKey] = true
			if !v.IsNil() && v.Cap() > 0 && v.Type().Elem().Size() > 0 {
				first := v.Pointer()
				limit := first + uintptr(v.Cap())*v.Type().Elem().Size()
				s.slices = append(s.slices, readonlySlice{v.Type(), first, limit, name})
			}
			full := v.Slice(0, v.Cap())
			for i := 0; i < full.Len(); i++ {
				visit(full.Index(i))
			}
		case reflect.Struct:
			for i := 0; i < v.NumField(); i++ {
				visit(v.Field(i))
			}
		case reflect.Array:
			for i := 0; i < v.Len(); i++ {
				visit(v.Index(i))
			}
		}
	}
	visit(root.Elem())
	return nil
}
func (s *ReadonlyState) owner(v reflect.Value) string {
	for v.IsValid() && v.Kind() == reflect.Interface {
		if v.IsNil() {
			return ""
		}
		v = v.Elem()
	}
	if key, ok := readonlyObjectKey(v); ok {
		if owner := s.objects[key]; owner != "" {
			return owner
		}
	}
	if v.IsValid() && v.Kind() == reflect.Slice && !v.IsNil() {
		first := v.Pointer()
		for _, region := range s.slices {
			if v.Type() == region.typ && first >= region.first && first < region.limit {
				return region.owner
			}
		}
	}
	return ""
}

// CheckAssign protects the binding location itself. Rebinding a distinct alias
// is not mutation of the old shared object's contents.
func (s *ReadonlyState) CheckAssign(binding any) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if key, ok := readonlyObjectKey(reflect.ValueOf(binding)); ok {
		if name := s.roots[key]; name != "" {
			return &ReadonlyError{fmt.Sprintf("cannot assign to readonly value %q", name)}
		}
	}
	return nil
}

// CheckMutation protects the actual container being changed. container is a
// map/slice/pointer, or the address of a struct/array binding. kind is "field",
// "map" or "slice" and path is positioned source-path text, never evaluated.
func (s *ReadonlyState) CheckMutation(name string, binding, container any, path, kind string) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	owner := s.resolveOwner(binding, container)
	if owner == "" {
		return nil
	}
	if owner != name {
		return &ReadonlyError{fmt.Sprintf("cannot mutate readonly value %q through alias %q and path %s", owner, name, path)}
	}
	if kind == "field" {
		return &ReadonlyError{fmt.Sprintf("cannot mutate readonly value %q through field %s", owner, path)}
	}
	return &ReadonlyError{fmt.Sprintf("cannot mutate readonly value %q through %s path %s", owner, kind, path)}
}

// resolveOwner names the marked root reached by this binding or container, or
// "" when neither is readonly. The binding is consulted first so a root's own
// name wins over the name of an object it merely shares.
func (s *ReadonlyState) resolveOwner(binding, container any) string {
	if key, ok := readonlyObjectKey(reflect.ValueOf(binding)); ok {
		if owner := s.roots[key]; owner != "" {
			return owner
		}
	}
	return s.owner(reflect.ValueOf(container))
}

// CheckBuiltin protects the target of a collection builtin. The whole container
// is the operand there, so the diagnostic names the builtin rather than a path,
// and it names the marked owner rather than the alias the call used: append,
// copy, delete and clear reach the same object through any binding.
func (s *ReadonlyState) CheckBuiltin(binding, container any, builtin string) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	owner := s.resolveOwner(binding, container)
	if owner == "" {
		return nil
	}
	return &ReadonlyError{fmt.Sprintf("cannot mutate readonly value %q through %s", owner, builtin)}
}

// MustReadonly unwinds with the guard's typed error. Generated code reports
// nothing itself: the panic lets deferred code run and reaches the program
// boundary, which prints the diagnostic once and exits with the error's own
// status. There is no failure sink here, so two programs in one process cannot
// see each other's guard failures.
func MustReadonly(err error) {
	if err != nil {
		panic(err)
	}
}
