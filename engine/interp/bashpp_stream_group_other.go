//go:build !unix

package interp

import "context"

// Windows has no POSIX pgid. The worker protocol keeps the same exclusive
// job reservation; existing context cancellation and B14 pipe ownership apply.
func (r *Runner) bashPPStreamGroupLifecycle(ctx context.Context, group int) (func(), error) {
	return func() {}, nil
}
