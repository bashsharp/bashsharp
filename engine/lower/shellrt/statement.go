package shellrt

// TryValue evaluates one generated binding initializer. A checked value or
// readonly failure leaves the result at its zero value and returns the original
// error for the owning Program to report. The caller commits the binding only
// when err is nil; effects already performed by body are not rolled back.
//
// Source panics, channel cancellation and shell exits keep unwinding. In
// particular, implementing error or ExitStatus does not make a source panic a
// binding failure. Source recover must remain in its original deferred frame.
func TryValue[T any](body func() T) (value T, err error) {
	defer func() {
		switch failure := recover().(type) {
		case nil:
		case ValueAbort:
			err = failure
		case *ValueError:
			err = failure
		case *ReadonlyError:
			err = failure
		default:
			panic(failure)
		}
	}()
	return body(), nil
}

// BindingValue projects a binding for a typed print argument. An absent binding
// uses the source spelling supplied by the compiler. A present value retains
// its native type and identity, including a typed nil pointer or interface.
// Shell expansion has a different absent-binding fallback, supplied separately
// by its caller. This helper neither reports errors nor changes program status.
func BindingValue[T any](present bool, value T, fallback string) any {
	if !present {
		return fallback
	}
	return value
}
