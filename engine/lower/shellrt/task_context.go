package shellrt

import "context"

type taskContextKey struct{}

// InTask reports that ctx belongs to a Session.Go body (or its descendants).
// The marker is inherited through derived contexts and cancellation-free
// cleanup, so a backend can retain task I/O policy during EXIT traps.
// It carries no channel, callable or agentic authority.
func InTask(ctx context.Context) bool {
	task, _ := ctx.Value(taskContextKey{}).(bool)
	return task
}

func ownedTaskContext(ctx context.Context) context.Context {
	return context.WithValue(ctx, taskContextKey{}, true)
}
