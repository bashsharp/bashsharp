// Copyright (c) 2026, the outpost authors
// See LICENSE for licensing information

//go:build !unix && !windows

package interp

import "context"

func (r *Runner) runNohup(ctx context.Context, args []string) exitStatus {
	r.errf("nohup: not supported on this platform\n")
	return exitStatus{code: 1}
}
