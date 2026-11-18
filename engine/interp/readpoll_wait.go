// Copyright (c) 2026, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package interp

import (
	"context"
	"errors"
	"os"
	"time"
)

// pollWaitCap bounds a single readiness wait so a cancelled context and a
// pending signal are still noticed by a read that asked for no deadline.
const pollWaitCap = 100 * time.Millisecond

// deadlinePoller drives the readiness loop for descriptors whose blocking
// read cannot be called off with (*os.File).SetReadDeadline — on Windows that
// is every console handle and every anonymous pipe, neither of which the Go
// runtime poller accepts. waitReady blocks for at most the duration it is
// given and reports whether the descriptor became readable; the loop around it
// owns the clock, the wake channel and the context, so the same logic is
// exercised by the tests on any host.
type deadlinePoller struct {
	ctx      context.Context
	deadline time.Time
	wake     <-chan struct{}

	waitReady func(time.Duration) (bool, error)

	// now defaults to time.Now; tests drive a fake clock through it.
	now func() time.Time
}

func (p *deadlinePoller) clock() time.Time {
	if p.now != nil {
		return p.now()
	}
	return time.Now()
}

// waitReadable returns nil once the caller should perform the real read.
// os.ErrDeadlineExceeded means the read timed out, errReadInterrupted that a
// signal wants the read abandoned, and a context error that the runner is
// going away.
func (p *deadlinePoller) waitReadable() error {
	for {
		// Propagate a real cancellation, but not a plain deadline — an
		// expired deadline still gets one probe below, so input that is
		// already buffered is read first, as bash does.
		if err := p.ctx.Err(); err != nil && !errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		wait := pollWaitCap
		if !p.deadline.IsZero() {
			remaining := p.deadline.Sub(p.clock())
			if remaining < 0 {
				remaining = 0
			}
			if remaining < wait {
				wait = remaining
			}
		}
		ready, err := p.waitReady(wait)
		if err != nil {
			return err
		}
		if ready {
			return nil
		}
		if p.wake != nil {
			select {
			case <-p.wake:
				return errReadInterrupted
			default:
			}
		}
		// Nothing ready; only give up once the deadline has truly passed.
		if !p.deadline.IsZero() && !p.clock().Before(p.deadline) {
			return os.ErrDeadlineExceeded
		}
	}
}
