// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

// Sprint: #270; Story: #757; Story-ID: 609c89bfa598
//
// Interpreter collection pacing for Go-source runs. The evaluator keeps a
// small live heap but allocates short-lived cells, frames and strings at a
// high rate, so at the default GOGC=100 the host process collects hundreds of
// times per second; the corpus profiles spent more time starting and stopping
// the world, preempting and re-committing scavenged spans than in the
// evaluator itself. While at least one Go-source program runs, the host's
// collector target is raised. This is the interpreter's own heap only: the
// program's runtime (runtime.GC, MemStats, SetGCPercent, finalizers observed
// through the dependency helper) is untouched, and an explicit GOGC in the
// host environment is always honoured. A memory-aware soft limit bounds the
// raised target independently of GOGC; explicit GOMEMLIMIT wins. Both knobs
// are restored when the last overlapping run exits.

import (
	"os"
	"runtime/debug"
	"sync"
)

const goSourceInterpreterGCPercent = 400

var goSourceGCPacing struct {
	mu                      sync.Mutex
	active                  int
	saved                   int
	savedLimit              int64
	changedGC, changedLimit bool
}

// goSourceRaiseGCPacing raises the host collector target for one Go-source
// run and returns the matching release.
func goSourceRaiseGCPacing() func() {
	p := &goSourceGCPacing
	p.mu.Lock()
	if p.active == 0 {
		_, explicitGC := os.LookupEnv("GOGC")
		p.changedGC = !explicitGC
		if p.changedGC {
			p.saved = debug.SetGCPercent(goSourceInterpreterGCPercent)
			if p.saved < 0 || p.saved > goSourceInterpreterGCPercent {
				debug.SetGCPercent(p.saved)
			}
		}
		_, explicitLimit := os.LookupEnv("GOMEMLIMIT")
		p.changedLimit = !explicitLimit
		if p.changedLimit {
			p.savedLimit = debug.SetMemoryLimit(-1)
			debug.SetMemoryLimit(min(p.savedLimit, goSourceMemoryBudget()))
		}
	}
	p.active++
	p.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			p.mu.Lock()
			p.active--
			if p.active == 0 {
				if p.changedGC {
					debug.SetGCPercent(p.saved)
				}
				if p.changedLimit {
					debug.SetMemoryLimit(p.savedLimit)
				}
			}
			p.mu.Unlock()
		})
	}
}
