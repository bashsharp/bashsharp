// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

// fakeClock drives deadlinePoller without sleeping, so the loop's decisions
// are tested on every host rather than only where the real probe compiles.
type fakeClock struct {
	now time.Time
}

func (c *fakeClock) advance(d time.Duration) { c.now = c.now.Add(d) }

func TestDeadlinePollerReadyWins(t *testing.T) {
	t.Parallel()
	clock := &fakeClock{now: time.Unix(0, 0)}
	calls := 0
	p := &deadlinePoller{
		ctx:      context.Background(),
		deadline: clock.now.Add(time.Second),
		now:      func() time.Time { return clock.now },
		waitReady: func(d time.Duration) (bool, error) {
			calls++
			return true, nil
		},
	}
	if err := p.waitReadable(); err != nil {
		t.Fatalf("want a readable descriptor, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("want a single probe, got %d", calls)
	}
}

func TestDeadlinePollerTimesOut(t *testing.T) {
	t.Parallel()
	clock := &fakeClock{now: time.Unix(0, 0)}
	deadline := clock.now.Add(250 * time.Millisecond)
	var waits []time.Duration
	p := &deadlinePoller{
		ctx:      context.Background(),
		deadline: deadline,
		now:      func() time.Time { return clock.now },
		waitReady: func(d time.Duration) (bool, error) {
			waits = append(waits, d)
			clock.advance(d)
			return false, nil
		},
	}
	err := p.waitReadable()
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("want os.ErrDeadlineExceeded, got %v", err)
	}
	// Each wait is capped by the time left, never overshooting the deadline.
	var total time.Duration
	for _, w := range waits {
		if w > pollWaitCap {
			t.Fatalf("wait %v exceeds the %v cap", w, pollWaitCap)
		}
		total += w
	}
	if total != 250*time.Millisecond {
		t.Fatalf("want the waits to sum to the timeout, got %v", total)
	}
}

// An expired deadline still gets one probe, so input that is already buffered
// is read rather than reported as a timeout — bash's own ordering.
func TestDeadlinePollerExpiredStillProbes(t *testing.T) {
	t.Parallel()
	clock := &fakeClock{now: time.Unix(0, 0)}
	calls := 0
	p := &deadlinePoller{
		ctx:      context.Background(),
		deadline: clock.now.Add(-time.Second),
		now:      func() time.Time { return clock.now },
		waitReady: func(d time.Duration) (bool, error) {
			calls++
			if d != 0 {
				t.Errorf("want a non-blocking probe, got a %v wait", d)
			}
			return true, nil
		},
	}
	if err := p.waitReadable(); err != nil {
		t.Fatalf("want a readable descriptor, got %v", err)
	}
	if calls != 1 {
		t.Fatalf("want a single probe, got %d", calls)
	}
}

func TestDeadlinePollerExpiredWithoutInput(t *testing.T) {
	t.Parallel()
	clock := &fakeClock{now: time.Unix(0, 0)}
	p := &deadlinePoller{
		ctx:      context.Background(),
		deadline: clock.now.Add(-time.Second),
		now:      func() time.Time { return clock.now },
		waitReady: func(time.Duration) (bool, error) {
			return false, nil
		},
	}
	if err := p.waitReadable(); !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("want os.ErrDeadlineExceeded, got %v", err)
	}
}

// Without a deadline the loop still wakes regularly, so a cancelled context
// and a pending signal are noticed by an otherwise indefinite read.
func TestDeadlinePollerNoDeadlineCapsTheWait(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	p := &deadlinePoller{
		ctx: ctx,
		waitReady: func(d time.Duration) (bool, error) {
			if d != pollWaitCap {
				t.Errorf("want a %v wait, got %v", pollWaitCap, d)
			}
			calls++
			if calls == 3 {
				cancel()
			}
			return false, nil
		},
	}
	if err := p.waitReadable(); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}

func TestDeadlinePollerWakeInterrupts(t *testing.T) {
	t.Parallel()
	wake := make(chan struct{})
	close(wake)
	p := &deadlinePoller{
		ctx:  context.Background(),
		wake: wake,
		waitReady: func(time.Duration) (bool, error) {
			return false, nil
		},
	}
	if err := p.waitReadable(); !errors.Is(err, errReadInterrupted) {
		t.Fatalf("want errReadInterrupted, got %v", err)
	}
}

// A ready descriptor beats a pending wake-up: the bytes are already there.
func TestDeadlinePollerReadyBeatsWake(t *testing.T) {
	t.Parallel()
	wake := make(chan struct{})
	close(wake)
	p := &deadlinePoller{
		ctx:  context.Background(),
		wake: wake,
		waitReady: func(time.Duration) (bool, error) {
			return true, nil
		},
	}
	if err := p.waitReadable(); err != nil {
		t.Fatalf("want a readable descriptor, got %v", err)
	}
}

func TestDeadlinePollerProbeError(t *testing.T) {
	t.Parallel()
	want := errors.New("probe failed")
	p := &deadlinePoller{
		ctx: context.Background(),
		waitReady: func(time.Duration) (bool, error) {
			return false, want
		},
	}
	if err := p.waitReadable(); !errors.Is(err, want) {
		t.Fatalf("want %v, got %v", want, err)
	}
}

// A read blocked on a pipe must end when the runner's context does, on every
// host: unix calls it off with SetReadDeadline, Windows by polling the handle
// the runtime poller refused.
func TestReadLineFromEndsWithTheContext(t *testing.T) {
	t.Parallel()
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pr.Close()
	defer pw.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, readErr := (&Runner{}).readLineFrom(ctx, pr, false, '\n')
		done <- readErr
	}()
	// Give the read time to block before taking it away.
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatalf("want the cancelled read to report an error")
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("the read outlived its context")
	}
}

// Cancelling must not cost the bytes that were already there.
func TestReadLineFromReadsBeforeCancel(t *testing.T) {
	t.Parallel()
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pr.Close()
	defer pw.Close()

	if _, err := pw.WriteString("one\n"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	line, err := (&Runner{}).readLineFrom(ctx, pr, false, '\n')
	if err != nil {
		t.Fatal(err)
	}
	if got := string(line); got != "one" {
		t.Fatalf("want %q, got %q", "one", got)
	}
}
