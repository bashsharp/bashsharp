// Copyright (c) 2026, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package interp

import (
	"context"
	"errors"
	"os"
	"time"
)

// readErrorIsTimeout reports whether a failed read exhausted its timeout.
// The read error is deliberately required: a descriptor can become readable
// just before its context deadline and return a complete line just after it.
// Bash keeps that line and succeeds; observing only ctx.Err after the read
// would discard already-available input, which is especially easy with the
// millisecond timeout in bash's read2.sub fixture on Windows.
func readErrorIsTimeout(ctx context.Context, err error, timeout time.Duration) bool {
	return timeout > 0 && err != nil &&
		(errors.Is(ctx.Err(), context.DeadlineExceeded) ||
			errors.Is(err, os.ErrDeadlineExceeded))
}
