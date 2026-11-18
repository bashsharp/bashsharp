package interp

import "context"

type taskPolicyKey struct{}

// WithTaskPolicy applies the interpreter's in-process task I/O and process
// isolation policy to work owned by an external structured task runtime.
// It does not transfer task ownership, create workers, or suppress Run's
// ordinary descendant/EOF cleanup. The policy follows derived contexts and
// lasts only for the Run receiving that context.
func WithTaskPolicy(ctx context.Context) context.Context {
	return context.WithValue(ctx, taskPolicyKey{}, true)
}

func taskPolicy(ctx context.Context) bool {
	enabled, _ := ctx.Value(taskPolicyKey{}).(bool)
	return enabled
}

func (r *Runner) inBashPPTask() bool { return r.bashPPGoTask || r.bashPPHostedTask }
