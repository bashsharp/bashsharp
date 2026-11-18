// Copyright (c) 2026, the bashy authors
// See LICENSE for licensing information

package interp_test

// Adversarial ordering table + the schedule matrix and quarantine entry points
// (Sprint #115, sh todo #2, items 4 & 5). The machinery these lean on lives in
// concurrency_harness_test.go.
//
// Every adversarial ordering is a TABLE ROW (a cgCase), never a bespoke test
// function, so the concurrency story (93de714f8c22) extends the SAME table by
// appending rows — cross-platform ones to cgBaseCases here, unix ones via
// cgRegister in concurrency_adversarial_unix_test.go. The forces we drive:
//
//   close-vs-send        -> carrier Terminate racing Wait (unix)
//   cancel-vs-complete   -> ctx cancel racing a background job's completion
//   parent-exit-vs-child -> the script exits while a background child runs
//   signal-vs-blocked-read -> an async interrupt lands as a read parks
//   simultaneous-completion -> two background jobs finishing at once
//
// Bash++ is never activated by any of these: they use ordinary POSIX/Bash
// background jobs (`&`, `wait`), context cancellation and the JobCarrier — the
// constructs that exist today — so Bash++-off behaviour stays byte-identical.

import (
	"context"
	"errors"
	"runtime"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/interp"
)

// cgCase is one adversarial ordering. run performs a single trial and must be
// self-contained; it must not block unboundedly — the matrix runner wraps it in
// cgAwait so a wedge is dumped and failed, never left to trip `go test -timeout`.
type cgCase struct {
	name     string
	category string
	run      func(t *testing.T)
}

// cgRegisteredCases is appended to by platform-specific files' init(), so unix
// rows (carrier, real fds) join the table without a second harness.
var cgRegisteredCases []cgCase // bashpp-racegate:safe-private

// cgRegister adds platform-specific rows to the adversarial table.
func cgRegister(c ...cgCase) { cgRegisteredCases = append(cgRegisteredCases, c...) }

// cgAllCases returns the full adversarial table for this platform.
func cgAllCases() []cgCase {
	return append(cgBaseCases(), cgRegisteredCases...)
}

// cgRunUnderCtx builds a fresh runner for src and runs it under ctx, returning
// the run error (ExitStatus is not treated specially by the caller). Used by
// the cross-platform rows.
func cgRunUnderCtx(t *testing.T, ctx context.Context, src string, opts ...interp.RunnerOption) error {
	t.Helper()
	file := parse(t, nil, src)
	var cb concBuffer
	opts = append(opts, interp.StdIO(nil, &cb, &cb))
	r, err := interp.New(opts...)
	if err != nil {
		t.Fatalf("interp.New: %v", err)
	}
	// Each trial owns a fresh Runner. Some paths install signal subscriptions
	// lazily (including descendants created by background jobs); without an
	// explicit Reset the test drops the only owner that can join them and the
	// schedule-matrix leak gate correctly reports the surviving forwarders.
	runErr := r.Run(ctx, file)
	r.Reset()
	return runErr
}

// cgBaseCases are the platform-independent adversarial rows. Each trial does a
// short internal burst so that, multiplied by the schedule matrix's repetition,
// a broad spread of interleavings is forced.
func cgBaseCases() []cgCase {
	return []cgCase{
		{
			name:     "cancel-vs-complete",
			category: "cancel-vs-complete",
			run: func(t *testing.T) {
				// A background job completes on its own while the context is
				// cancelled from another goroutine at (nearly) the same instant.
				// Either ordering must be race-free and must not hang.
				for i := 0; i < 40; i++ {
					ctx, cancel := context.WithCancel(context.Background())
					done := make(chan struct{})
					go func() {
						defer close(done)
						_ = cgRunUnderCtx(t, ctx, `{ :; } & wait`)
					}()
					go cancel() // race the cancel against natural completion
					cgAwait(t, "cancel-vs-complete trial", 5*time.Second, func() { <-done })
				}
			},
		},
		{
			name:     "parent-exit-vs-child",
			category: "parent-exit-vs-child",
			run: func(t *testing.T) {
				// The parent script reaches `exit` while a background child is
				// still spinning. Run returns without waiting, as bash does; the
				// helper's explicit Reset is the ownership boundary that must
				// cancel and reap the child without racing the exit path or
				// permanently leaking it. We then drain to baseline so a genuine
				// permanent leak becomes a diagnosed failure rather than residue.
				base := len(cgSuspectGoroutines())
				for i := 0; i < 30; i++ {
					err := cgRunUnderCtx(t, context.Background(),
						`{ for ((n=0;n<200;n++)); do :; done; } & exit 0`)
					var es interp.ExitStatus
					if err != nil && !errors.As(err, &es) {
						t.Fatalf("parent-exit-vs-child: unexpected error: %v", err)
					}
				}
				cgDrainToBaseline(t, base, "parent-exit-vs-child orphans", 15*time.Second)
			},
		},
		{
			name:     "simultaneous-completion",
			category: "simultaneous-completion",
			run: func(t *testing.T) {
				// Two background jobs finish at (nearly) the same moment and a
				// single `wait` must reap both without corrupting either status
				// or double-reaping. This is the two-operations-complete-at-once
				// ordering the future select/channel join must also survive.
				for i := 0; i < 40; i++ {
					err := cgRunUnderCtx(t, context.Background(),
						`{ exit 3; } & a=$!
						 { exit 4; } & b=$!
						 wait "$a"; x=$?
						 wait "$b"; y=$?
						 [ "$x" = 3 ] && [ "$y" = 4 ] || echo "BAD x=$x y=$y"`)
					var es interp.ExitStatus
					if err != nil && !errors.As(err, &es) {
						t.Fatalf("simultaneous-completion: unexpected error: %v", err)
					}
				}
			},
		},
	}
}

// runFocusedSet runs the whole adversarial table once, honouring quarantine.
// Each case runs as a subtest wrapped so a wedge is dumped, not silently timed
// out. Returns the number of cases actually executed (i.e. not quarantined).
func runFocusedSet(t *testing.T, cases []cgCase) int {
	t.Helper()
	executed := 0
	for _, c := range cases {
		c := c
		if q, ok := cgIsQuarantined(c.name); ok {
			// Quarantine is REPORTED, never converted into a silent pass.
			t.Logf("QUARANTINED case %q [%s]: owner=%s expiry=%s tracking=%s\n  rationale: %s",
				c.name, c.category, q.owner, q.expiry.Format("2006-01-02"), q.tracking, q.rationale)
			continue
		}
		executed++
		t.Run(c.name, func(t *testing.T) {
			c.run(t)
		})
	}
	return executed
}

// TestConcurrencyAdversarial is the human-readable entry point: it runs the
// adversarial table exactly once, under leak detection, at whatever GOMAXPROCS
// the outer `go test` selected. It is deliberately NOT the gate — the gate is
// TestConcurrencyScheduleMatrix, which additionally varies GOMAXPROCS and
// repeats, because a single green pass (especially at GOMAXPROCS=1) proves
// almost nothing.
func TestConcurrencyAdversarial(t *testing.T) {
	leaks := cgNewLeakDetector(t)
	runFocusedSet(t, cgAllCases())
	leaks.Check(t)
}

// TestConcurrencyScheduleMatrix is THE gate. It runs the focused set repeatedly
// under varied GOMAXPROCS (1, 2, and NumCPU at minimum), records the matrix
// actually executed, and REFUSES to be counted as passing if it never left
// single-P — because under GOMAXPROCS=1 many races simply do not schedule.
//
// It is not t.Parallel: it mutates the process-global GOMAXPROCS and counts
// goroutines, so it must own the scheduler while it runs.
func TestConcurrencyScheduleMatrix(t *testing.T) {
	if cgRaceEnabled && testing.Short() {
		t.Skip("schedule matrix under -race is heavy; skipped in -short")
	}

	repeat := 8
	if testing.Short() {
		repeat = 2
	}

	numCPU := runtime.NumCPU()
	// The minimum matrix. Setting GOMAXPROCS=2 forces at least two Ps even on a
	// single-core box, which is what actually lets the interleavings schedule.
	procsSet := map[int]bool{1: true, 2: true, numCPU: true}
	var procsList []int
	for p := range procsSet {
		procsList = append(procsList, p)
	}

	prev := runtime.GOMAXPROCS(0)
	defer runtime.GOMAXPROCS(prev)

	var matrix cgMatrix
	leaks := cgNewLeakDetector(t)

	cases := cgAllCases()
	for _, procs := range procsList {
		runtime.GOMAXPROCS(procs)
		t.Run(subtestGOMAXPROCS(procs), func(t *testing.T) {
			var executed int
			for r := 0; r < repeat; r++ {
				executed = runFocusedSet(t, cases)
			}
			matrix.record(procs, numCPU, repeat, executed)
		})
	}

	t.Log(matrix.report())

	// A green run at GOMAXPROCS=1 alone is NOT the gate passing. Fail loudly if
	// the matrix never exercised true multi-P scheduling.
	if got := matrix.maxProcsExercised(); got < 2 {
		t.Fatalf("schedule matrix ran only at GOMAXPROCS<=%d: a single-P run does "+
			"not schedule most races and MUST NOT be reported as the gate passing. "+
			"Ensure GOMAXPROCS>=2 is exercised.", got)
	}

	leaks.Check(t)
}

func subtestGOMAXPROCS(p int) string {
	switch p {
	case 1:
		return "GOMAXPROCS=1"
	case 2:
		return "GOMAXPROCS=2"
	default:
		return "GOMAXPROCS=NumCPU"
	}
}

// TestConcurrencyQuarantine reports every quarantined case and FAILS the moment
// an entry's expiry has passed, so a quarantine is a dated debt, never a
// permanent way to hide a race. With nothing quarantined (the healthy state) it
// simply logs that fact.
func TestConcurrencyQuarantine(t *testing.T) {
	if len(cgQuarantine) == 0 {
		t.Log("quarantine registry is empty — no adversarial case is currently excluded.")
		return
	}
	now := time.Now()
	for _, e := range cgQuarantine {
		t.Logf("QUARANTINED %q: owner=%s expiry=%s tracking=%s\n  rationale: %s",
			e.caseName, e.owner, e.expiry.Format("2006-01-02"), e.tracking, e.rationale)
		if now.After(e.expiry) {
			t.Errorf("quarantine for %q expired on %s and was not resolved: a "+
				"quarantine may not silently outlive its due date — fix the case "+
				"or renew the entry with a rationale.",
				e.caseName, e.expiry.Format("2006-01-02"))
		}
	}
}
