// Copyright (c) 2026, the bashy authors
// See LICENSE for licensing information

//go:build unix

package interp_test

// Unix adversarial rows: the ones that need a real kernel-visible carrier
// process or real file descriptors. They register into the same cgCase table as
// the cross-platform rows (concurrency_adversarial_test.go) via init, so there
// is exactly one adversarial table, not two.
//
// Deliberately NO process-wide os signals are raised here. This test binary
// runs many runners concurrently and each installs its own signal.Notify; a
// stray process-directed signal (e.g. an untrapped SIGUSR2 defaulting to
// terminate) would kill the whole binary and destroy every other test's
// diagnosis. Signal delivery is therefore exercised only against a DEDICATED
// carrier child process, whose signals cannot leak to sibling tests. The
// in-process, os-signal path (trapped SIGUSR1 interrupting read) is already
// covered by TestReadInterruptedByTrappedSignal.

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/interp"
)

func init() {
	cgRegister(
		cgCase{
			name:     "carrier-close-vs-send",
			category: "close-vs-send",
			run:      cgCarrierCloseVsSend,
		},
		cgCase{
			name:     "async-interrupt-vs-blocked-read",
			category: "signal-vs-blocked-read",
			run:      cgAsyncInterruptVsBlockedRead,
		},
	)
}

// cgCarrierCloseVsSend drives the JobCarrier contract's hardest ordering: the
// carrier says Terminate must be idempotent and safe to call concurrently with
// Wait (carrier.go). This is the close-vs-send analogue — a "close" (Terminate)
// racing the terminal "send" (the carrier's own exit that Wait observes), plus
// concurrent redundant closes. We reap directly rather than through a runner so
// the contract is tested in isolation.
func cgCarrierCloseVsSend(t *testing.T) {
	for i := 0; i < 20; i++ {
		c := new(testCarrier)
		proc, err := c.StartCarrier(context.Background())
		if err != nil {
			t.Fatalf("StartCarrier: %v", err)
		}
		if proc.Pid() <= 0 {
			t.Fatalf("carrier Pid() = %d, want positive", proc.Pid())
		}

		// One goroutine reaps (the single permitted Wait); several race
		// Terminate against it and against each other.
		waited := make(chan int, 1)
		go func() { waited <- proc.Wait() }()

		var wg sync.WaitGroup
		for k := 0; k < 4; k++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				proc.Terminate()
			}()
		}

		cgAwait(t, "carrier Terminate storm", 5*time.Second, wg.Wait)
		// Reap the single Wait through the value-returning bounded wait, so a
		// wedged Wait is dumped rather than lost to `go test -timeout`. The
		// signal number is not asserted (Terminate may win by close-of-stdin or
		// by kill); the point is that Wait returns exactly once, race-free.
		_ = cgDeadline(t, "carrier Wait after Terminate", 5*time.Second, func() int { return <-waited })

		// A late, redundant Terminate after reap must be a safe no-op.
		proc.Terminate()
	}
}

// cgAsyncInterruptVsBlockedRead parks a `read` on a real pipe with no data and
// then fires a context cancellation as the read is parking — the in-process
// equivalent of a signal landing on a blocked read. A real fd (os.Pipe) is used
// so the runner's readpoll path genuinely observes cancellation. To guarantee
// the trial can never hang even if a given ordering fails to interrupt, the
// write end is also closed shortly after, so EOF unblocks the read as a
// backstop; the assertion is only that no ordering races or wedges.
func cgAsyncInterruptVsBlockedRead(t *testing.T) {
	for i := 0; i < 30; i++ {
		pr, pw, err := os.Pipe()
		if err != nil {
			t.Fatalf("os.Pipe: %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())

		file := parse(t, nil, `read x; :`)
		var cb concBuffer
		r, nerr := interp.New(interp.StdIO(pr, &cb, &cb))
		if nerr != nil {
			t.Fatalf("interp.New: %v", nerr)
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			_ = r.Run(ctx, file)
		}()

		// Race the interrupt against the read parking.
		go cancel()
		go func() {
			// Backstop so the trial is bounded regardless of ordering.
			time.Sleep(2 * time.Millisecond)
			_ = pw.Close()
		}()

		cgAwait(t, "blocked-read interrupt trial", 5*time.Second, func() { <-done })
		cancel()
		_ = pw.Close()
		_ = pr.Close()
		r.Reset()
	}
}
