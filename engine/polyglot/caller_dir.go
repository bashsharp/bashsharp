package polyglot

import "context"

type callerDirKey struct{}

// WithCallerDir binds a foreign call to its calling shell session. It does not
// change the process directory or the shared module's runtime configuration.
func WithCallerDir(ctx context.Context, dir string) context.Context {
	return context.WithValue(ctx, callerDirKey{}, dir)
}
