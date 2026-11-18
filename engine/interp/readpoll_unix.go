// Copyright (c) 2026, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

//go:build unix

package interp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// timeoutReader wraps f in a poll/select-based reader that honours the
// deadline. On unix this is always reliable (terminals, pipes, fifos),
// whereas (*os.File).SetReadDeadline silently no-ops for fds not registered
// with the runtime poller — e.g. a fifo opened via `exec 9<>p` on linux,
// which would otherwise make `read -u 9 -t …` block past its timeout.
func timeoutReader(ctx context.Context, f *os.File, deadline time.Time) io.Reader {
	return &timeoutFileReader{ctx: ctx, file: f, deadline: deadline}
}

func signalReader(ctx context.Context, f *os.File, wake <-chan struct{}) io.Reader {
	return &timeoutFileReader{ctx: ctx, file: f, wake: wake}
}

func fdReadableNow(f *os.File) bool {
	pollFd := []unix.PollFd{{
		Fd:     int32(f.Fd()),
		Events: unix.POLLIN | unix.POLLHUP | unix.POLLERR,
	}}
	n, err := unix.Poll(pollFd, 0)
	return err == nil && n > 0 && pollFd[0].Revents != 0
}

func taskReadReadyNow(f *os.File) bool {
	raw, err := f.SyscallConn()
	if err != nil {
		return false
	}
	ready := false
	if err := raw.Control(func(fd uintptr) {
		pollFd := []unix.PollFd{{
			Fd:     int32(fd),
			Events: unix.POLLIN | unix.POLLHUP | unix.POLLERR,
		}}
		n, pollErr := unix.Poll(pollFd, 0)
		ready = pollErr == nil && n > 0 && pollFd[0].Revents != 0
	}); err != nil {
		return false
	}
	return ready
}

// taskReadReader returns a reader only when the descriptor is already in
// nonblocking mode. Each actual read is a raw nonblocking attempt performed
// under SyscallConn.Control; an alias which drains readiness first therefore
// produces EAGAIN rather than stranding the task in a blocking File.Read.
func taskReadReader(ctx context.Context, f *os.File, deadline time.Time, wake <-chan struct{}, beforeBlock func() bool) io.Reader {
	raw, err := f.SyscallConn()
	if err != nil {
		return nil
	}
	supported := false
	if err := raw.Control(func(fd uintptr) {
		flags, flagErr := unix.FcntlInt(fd, unix.F_GETFL, 0)
		supported = flagErr == nil && flags&unix.O_NONBLOCK != 0
	}); err != nil || !supported {
		return nil
	}
	return &taskNonblockingReader{
		ctx: ctx, raw: raw, deadline: deadline, wake: wake, beforeBlock: beforeBlock,
	}
}

type taskNonblockingReader struct {
	ctx         context.Context
	raw         syscall.RawConn
	deadline    time.Time
	wake        <-chan struct{}
	beforeBlock func() bool
	armed       bool
}

func (r *taskNonblockingReader) Read(p []byte) (int, error) {
	for {
		if err := r.ctx.Err(); err != nil {
			return 0, err
		}
		if r.wake != nil {
			select {
			case <-r.wake:
				return 0, errReadInterrupted
			default:
			}
		}
		var n int
		var readErr error
		supported := false
		controlErr := r.raw.Control(func(fd uintptr) {
			flags, flagErr := unix.FcntlInt(fd, unix.F_GETFL, 0)
			if flagErr != nil {
				readErr = flagErr
				return
			}
			if flags&unix.O_NONBLOCK == 0 {
				// A concurrent F_SETFL can still race this check and the raw
				// read. Closing that external mutation window requires the
				// resource/action ownership gate; until then P4 remains blocked.
				return
			}
			supported = true
			n, readErr = unix.Read(int(fd), p)
		})
		if controlErr != nil {
			return 0, controlErr
		}
		if !supported {
			return 0, fmt.Errorf("descriptor lost nonblocking task-read mode")
		}
		switch {
		case readErr == nil && n == 0:
			return 0, io.EOF
		case readErr == nil:
			return n, nil
		case readErr == unix.EINTR:
			continue
		case readErr != unix.EAGAIN && readErr != unix.EWOULDBLOCK:
			return n, readErr
		}
		// As with Bash's timeout probe, ready data and EOF win over an expired
		// deadline; only a real EAGAIN observes the clock.
		if !r.deadline.IsZero() && time.Until(r.deadline) <= 0 {
			return 0, os.ErrDeadlineExceeded
		}
		if !r.armed {
			if !r.beforeBlock() {
				if err := r.ctx.Err(); err != nil {
					return 0, err
				}
				return 0, context.Canceled
			}
			r.armed = true
		}
		// Five-millisecond retries bound group-cancellation and signal latency
		// without retaining the raw descriptor outside Control.
		wait := 5 * time.Millisecond
		if !r.deadline.IsZero() && time.Until(r.deadline) < wait {
			wait = max(time.Until(r.deadline), 0)
		}
		timer := time.NewTimer(wait)
		select {
		case <-r.ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return 0, r.ctx.Err()
		case <-r.wake:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return 0, errReadInterrupted
		case <-timer.C:
		}
	}
}

type timeoutFileReader struct {
	ctx      context.Context
	file     *os.File
	deadline time.Time
	wake     <-chan struct{}
}

func (r *timeoutFileReader) Read(p []byte) (int, error) {
	fd := int(r.file.Fd())
	if fd < 0 {
		return 0, os.ErrInvalid
	}
	for {
		// Propagate a real cancellation, but not a plain deadline — a passed
		// deadline still gets one non-blocking poll below so already-buffered
		// input is read first.
		if err := r.ctx.Err(); err != nil && !errors.Is(err, context.DeadlineExceeded) {
			return 0, err
		}
		// Wait for readability up to the deadline. A non-positive remaining
		// still does one non-blocking poll (msec 0), so data that is already
		// available — a here-string, or a fifo with bytes ready — is read
		// even when the timeout is tiny or already elapsed, matching bash
		// (ready input is consumed before a -t timeout is reported). poll(2)
		// is used uniformly (no FD_SETSIZE limit, unlike select).
		msec := 100
		if !r.deadline.IsZero() {
			remaining := time.Until(r.deadline)
			msec = int(remaining.Milliseconds())
			if msec < 0 {
				msec = 0
			} else if msec == 0 && remaining > 0 {
				msec = 1 // sub-millisecond remaining: round up, don't busy-spin
			}
		}
		pollFd := []unix.PollFd{{
			Fd:     int32(fd),
			Events: unix.POLLIN | unix.POLLHUP | unix.POLLERR,
		}}
		n, err := unix.Poll(pollFd, int(msec))
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return 0, err
		}
		if n > 0 && pollFd[0].Revents != 0 {
			// Readable (data, EOF, or hangup) — let the real read decide.
			return r.file.Read(p)
		}
		if r.wake != nil {
			select {
			case <-r.wake:
				return 0, errReadInterrupted
			default:
			}
		}
		// Nothing ready; only give up once the deadline has truly passed.
		if !r.deadline.IsZero() && time.Until(r.deadline) <= 0 {
			return 0, os.ErrDeadlineExceeded
		}
	}
}

// cancellableReader has nothing to add here: every descriptor this shell
// reads from is registered with the runtime poller, so the caller's
// SetReadDeadline already calls a blocked read off.
func cancellableReader(context.Context, *os.File) io.Reader { return nil }
