// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"context"
	"errors"
	"time"
)

// bashPPTimedDecoratorName is the engine-supplied observation decorator
// (Sprint 221, B27a). Unlike @go.error it is an ordinary rung, not a marker:
// it runs the rest of the chain exactly once and reports how long that took,
// changing nothing a decorator or the caller observes. It resolves only after
// every user-owned slot — a script-declared typed decorator, an embedder
// native, even the shell-function EDECO-SIG refusal — so it occupies the slot
// EDECO-UNDEF previously reported and can never take a name from user code.
const bashPPTimedDecoratorName = "timed"

// bashPPTimedDecorator builds the @timed rung for one invocation. The
// attestation line is written to the runner's stderr from a deferred
// observation, so an erroring body and an unwinding panic are timed exactly
// like a success (the Python try/finally timing-wrapper shape; timeit's timer
// choice pins the monotonic clock, which time.Since reads). The line's shape
// is stable — `@timed: <name>: status=<N> duration=<D>` — with only the
// measured duration varying run to run.
func bashPPTimedDecorator(r *Runner) DecoratorFunc {
	return func(ctx context.Context, c *Call, args []DecoratorArg) error {
		if len(args) != 0 {
			return errors.New("requires no arguments")
		}
		start := time.Now()
		defer func() {
			r.errf("@timed: %s: status=%d duration=%s\n", c.Name, c.Status, time.Since(start))
		}()
		c.Next(ctx)
		return nil
	}
}
