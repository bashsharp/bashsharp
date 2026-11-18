// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"context"
	"runtime"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// bashPPStackSegmentCalls is how many nested Go-form calls share one host
// goroutine stack.
//
// The evaluator is recursive: every interpreted call level re-enters it on
// the host stack, at several KiB a level, so a Go program that recurses a few
// hundred thousand deep — which a native Go stack carries easily — would
// exceed the host's per-goroutine stack limit long before it ran out of
// memory. Every bashPPStackSegmentCalls levels the next function body
// therefore continues on a fresh host stack while the caller waits, so the
// interpreted depth is bounded by memory, as it is natively, and no single
// host stack grows without limit.
const bashPPStackSegmentCalls = 1024

// bashPPRunBody runs a Go-form function body, moving to a fresh host stack at
// each segment boundary.
func (r *Runner) bashPPRunBody(ctx context.Context, stmts []*syntax.Stmt) {
	if r.bashPPFuncActive%bashPPStackSegmentCalls != 0 {
		r.stmts(ctx, stmts)
		return
	}
	bashPPOnFreshStack(func() { r.stmts(ctx, stmts) })
}

// bashPPOnFreshStack runs fn to completion on a new goroutine and waits for
// it. The caller is blocked throughout, so fn still has exclusive use of
// whatever the caller owned. A host panic in fn is raised again in the caller
// with its original value, and a [runtime.Goexit] in fn ends the caller too,
// so the hop is invisible to deferred calls further up the host stack.
func bashPPOnFreshStack(fn func()) {
	var (
		done     = make(chan struct{})
		finished bool
		panicked bool
		value    any
	)
	go func() {
		defer close(done)
		defer func() {
			if finished {
				return
			}
			if p := recover(); p != nil {
				panicked, value = true, p
			}
		}()
		fn()
		finished = true
	}()
	<-done
	if panicked {
		panic(value)
	}
	if !finished {
		runtime.Goexit()
	}
}
