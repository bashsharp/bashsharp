package shellrt

import "reflect"

// GuardFailure reports a checked language operation, distinct from a source panic.
type GuardFailure interface {
	error
	ExitStatus() int
}

// GuardError identifies only runtime-owned aborts. Ordinary user error values
// remain ordinary panic payloads even if they expose an ExitStatus method.
func GuardError(value any) GuardFailure {
	switch v := value.(type) {
	case ValueAbort:
		return v
	case *ReadonlyError:
		return v
	case *ValueError:
		return v
	case *ProjectionError:
		return projectionFailure{v}
	}
	return nil
}

type projectionFailure struct{ *ProjectionError }

func (projectionFailure) ExitStatus() int { return 1 }

// PreserveAbort is applied to the result of a direct source recover call. The
// recover expression remains in the deferred function; checked failures keep
// unwinding to the generated entry boundary.
func PreserveAbort(value any) any {
	if _, ok := value.(DeclarationAbort); ok {
		panic(value)
	}
	if _, ok := value.(ExpansionAbort); ok {
		panic(value)
	}
	if _, ok := value.(ShellAbort); ok {
		panic(value)
	}
	if _, ok := value.(ShellExit); ok {
		panic(value)
	}
	if _, ok := value.(ChannelAbort); ok {
		panic(value)
	}
	if GuardError(value) != nil {
		panic(value)
	}
	return value
}

// AssertionPossible uses the source and target method sets, independently of
// the current interface payload. Interface targets may add methods, but cannot
// contradict a method already required by the source interface.
func AssertionPossible[Source, Target any]() bool {
	source, target := reflect.TypeFor[Source](), reflect.TypeFor[Target]()
	if source.Kind() != reflect.Interface {
		return false
	}
	if target.Kind() != reflect.Interface {
		return target.Implements(source)
	}
	for i := 0; i < source.NumMethod(); i++ {
		method := source.Method(i)
		if other, ok := target.MethodByName(method.Name); ok && method.PkgPath == other.PkgPath && method.Type != other.Type {
			return false
		}
	}
	return true
}
