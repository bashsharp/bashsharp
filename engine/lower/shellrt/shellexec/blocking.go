package shellexec

import (
	"context"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/lower/shellrt"
)

var _ shellrt.BlockingShellRunner = (*shell)(nil)

// RunShellWithBlocking keeps immediate builtins inside the current launch and
// delegates suspension notification to the interpreter's operation boundary.
func (sh *shell) RunShellWithBlocking(ctx context.Context, state *shellrt.State, streams shellrt.Stdio, source string, beforeBlock func()) error {
	return sh.RunShell(interp.WithBlockingObserver(ctx, beforeBlock), state, streams, source)
}

// CloseWithBlocking uses the same boundary while EXIT traps release resources.
func (sh *shell) CloseWithBlocking(ctx context.Context, beforeBlock func()) error {
	return sh.Close(interp.WithBlockingObserver(ctx, beforeBlock))
}
