// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

// Sprint: #270; Story: #757; Story-ID: 609c89bfa598

import (
	"os"
	"runtime/debug"
	"testing"
)

func TestGoSourceGCPacingNestsAndRestores(t *testing.T) {
	if _, set := os.LookupEnv("GOGC"); set {
		t.Skip("GOGC is set in the host environment; pacing defers to it")
	}
	orig := debug.SetGCPercent(100)
	defer debug.SetGCPercent(orig)
	outer := goSourceRaiseGCPacing()
	inner := goSourceRaiseGCPacing()
	if got := debug.SetGCPercent(goSourceInterpreterGCPercent); got != goSourceInterpreterGCPercent {
		t.Fatalf("GC percent while running = %d, want %d", got, goSourceInterpreterGCPercent)
	}
	inner()
	inner() // a release is idempotent
	if got := debug.SetGCPercent(goSourceInterpreterGCPercent); got != goSourceInterpreterGCPercent {
		t.Fatalf("GC percent after inner release = %d, want %d", got, goSourceInterpreterGCPercent)
	}
	outer()
	if got := debug.SetGCPercent(100); got != 100 {
		t.Fatalf("GC percent after release = %d, want 100", got)
	}
	// A laxer host setting is kept.
	debug.SetGCPercent(1000)
	release := goSourceRaiseGCPacing()
	if got := debug.SetGCPercent(1000); got != 1000 {
		t.Fatalf("laxer host GC percent = %d, want 1000", got)
	}
	release()
	if got := debug.SetGCPercent(100); got != 1000 {
		t.Fatalf("restored GC percent = %d, want 1000", got)
	}
}

func TestGoSourceMemoryPacingRestoresAndHonorsHost(t *testing.T) {
	// These runtime knobs are process-global; never run this test in parallel.
	saved := debug.SetMemoryLimit(-1)
	defer debug.SetMemoryLimit(saved)
	old, present := os.LookupEnv("GOMEMLIMIT")
	os.Unsetenv("GOMEMLIMIT")
	defer func() {
		if present {
			os.Setenv("GOMEMLIMIT", old)
		} else {
			os.Unsetenv("GOMEMLIMIT")
		}
	}()
	debug.SetMemoryLimit(1<<63 - 1)
	outer := goSourceRaiseGCPacing()
	inner := goSourceRaiseGCPacing()
	if got := debug.SetMemoryLimit(-1); got != goSourceMemoryBudget() {
		t.Fatalf("limit=%d", got)
	}
	inner()
	inner()
	if got := debug.SetMemoryLimit(-1); got != goSourceMemoryBudget() {
		t.Fatalf("inner restored early: %d", got)
	}
	outer()
	if got := debug.SetMemoryLimit(-1); got != 1<<63-1 {
		t.Fatalf("not restored: %d", got)
	}
	debug.SetMemoryLimit(128 << 20)
	release := goSourceRaiseGCPacing()
	if got := debug.SetMemoryLimit(-1); got != 128<<20 {
		t.Fatalf("relaxed host limit: %d", got)
	}
	release()
	t.Setenv("GOMEMLIMIT", "off")
	debug.SetMemoryLimit(1<<63 - 1)
	release = goSourceRaiseGCPacing()
	if got := debug.SetMemoryLimit(-1); got != 1<<63-1 {
		t.Fatalf("overrode GOMEMLIMIT: %d", got)
	}
	release()
}
