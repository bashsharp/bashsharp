package interp

import "context"

type blockingObserverKey struct{}

// WithBlockingObserver returns a context which notifies before an interpreter
// operation reaches a semantic blocking boundary, such as an external command
// or a waiting read. An embedder can use this to release its task launcher
// without treating parsing or an immediate builtin as a suspension.
//
// The observer must return promptly. It belongs to this context, not a Runner
// or a process global, and is inherited by derived contexts. Nil disables it.
// The observer does not make arbitrary user handlers cancellable; handlers
// remain responsible for respecting their supplied context.
func WithBlockingObserver(ctx context.Context, observer func()) context.Context {
	return context.WithValue(ctx, blockingObserverKey{}, observer)
}

func observeBlocking(ctx context.Context) error {
	observer, _ := ctx.Value(blockingObserverKey{}).(func())
	if observer == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	observer()
	return ctx.Err()
}
