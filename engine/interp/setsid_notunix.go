// Copyright (c) 2026, the outpost authors
// See LICENSE for licensing information

//go:build !unix && !windows

package interp

import "context"

func (r *Runner) runSetsid(ctx context.Context, args []string) exitStatus {
	r.errf("setsid: not supported on this platform\n")
	return exitStatus{code: 1}
}

// runDetachedExec is unavailable on non-Unix — setsid and nohup both
// fail at the platform layer rather than silently degrading.
func runDetachedExec(ctx context.Context, r *Runner, label string, args []string, foreground bool) exitStatus {
	r.errf("%s: not supported on this platform\n", label)
	return exitStatus{code: 1}
}
