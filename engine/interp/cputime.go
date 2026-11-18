// Copyright (c) 2024, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package interp

import (
	"os"
	"sync"
	"time"
)

// timingScope accumulates the CPU time consumed by external child
// processes launched while the outermost `time` keyword clause runs.
//
// The shell process's own CPU — builtins, arithmetic, and in-process
// subshell goroutines (subshells are goroutines, not real forks in this
// pure-Go runner) — is captured separately by sampling the shell process
// CPU (getrusage RUSAGE_SELF on Unix, GetProcessTimes on Windows) before
// and after the clause. Only genuine external children are missed by that
// process-wide delta, so only their ProcessState CPU is folded in here.
//
// A pipeline's children are waited on concurrently (each stage runs on a
// subshell copy of the runner that shares this one scope by pointer), so
// aggregation is mutex-guarded. Nested `time` clauses share the single
// outermost scope, so each child's CPU is added exactly once and inner
// clauses cannot double-count it.
type timingScope struct {
	mu   sync.Mutex
	user time.Duration
	sys  time.Duration
}

// add folds one external child's user and system CPU into the scope. It is
// safe to call on a nil scope (outside any `time` clause) and from multiple
// goroutines.
func (t *timingScope) add(user, sys time.Duration) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.user += user
	t.sys += sys
	t.mu.Unlock()
}

// total returns the accumulated child CPU under the lock.
func (t *timingScope) total() (user, sys time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.user, t.sys
}

// processStateCPUTimes extracts CPU use from the ordinary os/exec wait path.
func processStateCPUTimes(ps *os.ProcessState) (user, sys time.Duration) {
	if ps == nil {
		return 0, 0
	}
	return ps.UserTime(), ps.SystemTime()
}

// accumulateChildCPU adds one external child's measured CPU usage to the
// runner's active timing scope, if any.
func (r *Runner) accumulateChildCPU(user, sys time.Duration) {
	if r.timing == nil {
		return
	}
	r.timing.add(user, sys)
}
