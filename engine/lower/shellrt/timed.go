package shellrt

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// timedDecoratorName is the engine-supplied observation decorator (Sprint
// 221, B27a), mirrored from the interpreter's bashPPTimedDecoratorName. A
// generated chain resolves it only after the process-level Decorators
// registry misses — a source-declared decorator named `timed` lowered
// statically and an embedder native both take precedence, exactly as in the
// interpreter — so the built-in occupies the slot EDECO-UNDEF previously
// reported and can never take a name from user code.
const timedDecoratorName = "timed"

// timedDecorator builds the @timed rung for one invocation of a compiled
// chain. The attestation line is written to the program's stderr from a
// deferred observation, so an erroring body and an unwinding panic are timed
// exactly like a success, and its shape matches the interpreter byte for
// byte apart from the measured duration:
// `@timed: <name>: status=<N> duration=<D>`.
func timedDecorator(p *Program) DecoratorFunc {
	return func(ctx context.Context, c *Call, args []DecoratorArg) error {
		if len(args) != 0 {
			return errors.New("requires no arguments")
		}
		start := time.Now()
		defer func() {
			fmt.Fprintf(p.stderr(), "@timed: %s: status=%d duration=%s\n", c.Name, c.Status, time.Since(start))
		}()
		c.Next(ctx)
		return nil
	}
}
