// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"bufio"
	"context"
	"errors"
	"io"
	"sync"
)

// The one internal bounded Go-channel process line substrate, shared by the
// dialect's live-process surface (B14 `start(...)`) and available to any other
// consumer that needs to stream a process's stdout as lines. It exists once so
// there is a single place where the hard parts are settled:
//
//   - THE PRODUCER OWNS CLOSE. Exactly one goroutine writes to the lines
//     channel and it is the only thing that closes it, when the process's
//     stdout reaches EOF or streaming stops. A consumer never closes the
//     channel, so a range over Lines() terminates cleanly and there is no
//     send-on-closed race.
//
//   - ERRORS STAY SEPARATE FROM DATA. A line is a line; a scanner/IO failure
//     or a cancellation is reported through Wait's error result, never smuggled
//     in as a data line. The process's own exit status (including a non-zero
//     code or a signal) is a STATUS, returned as the first result of Wait, not
//     an error — so `set -e`-style logic and "did it fail" are the caller's to
//     decide, exactly as with `run`/`capture`.
//
//   - EVERY PROCESS IS DRAINED AND REAPED. stdout and stderr are both drained
//     to completion — even when the consumer stops reading early — so the child
//     never blocks on a full pipe, and the underlying process is Wait'd exactly
//     once so it is reaped rather than left a zombie. This is what keeps a
//     partial consumer or a cancellation from leaking a goroutine or a process.
//
//   - THE CHANNEL IS BOUNDED. Its capacity is fixed at construction, so a slow
//     consumer applies backpressure to the producer instead of letting an
//     unbounded queue grow without limit.
//
//   - NOTHING BUT WAIT BLOCKS. Lines() just hands back the channel and Stderr()
//     returns already-drained bytes; neither waits. Wait is the one blocking
//     call, and it is exact-once: repeated calls return the identical
//     (status, error) and reap the process only the first time.

// bashPPProcessSource is a process that has already been started, with its
// output streams available and a way to reap and to terminate it. Abstracting
// it this way lets the same substrate stream a real os/exec process (the
// ported os/exec lifecycle tests) and the dialect's in-process subshell (the
// `start(...)` surface) without duplicating the channel/drain/reap logic.
type bashPPProcessSource interface {
	// Stdout is the process's standard output, read to EOF by the producer.
	Stdout() io.Reader
	// Stderr is the process's standard error, drained concurrently. It may be
	// nil when stderr is not captured (it then passes through elsewhere).
	Stderr() io.Reader
	// Wait blocks until the process exits and reaps it. The substrate calls it
	// exactly once; its error is interpreted for the exit status.
	Wait() error
	// Kill terminates the process (best effort). It may be called more than
	// once and after the process has already exited.
	Kill()
}

// bashPPLineProcess streams a started process's stdout as lines over a bounded
// channel while draining stderr and guaranteeing the process is reaped once.
type bashPPLineProcess struct {
	src    bashPPProcessSource
	ctx    context.Context
	cancel context.CancelFunc

	lines chan string

	producerDone chan struct{}
	stderrDone   chan struct{}
	stderrBuf    []byte

	killOnce sync.Once

	mu      sync.Mutex
	scanErr error // a stdout scan/IO failure, kept apart from the data lines

	waitOnce sync.Once
	status   int
	waitErr  error
}

// bashPPDefaultLineBuffer is the fixed capacity of the bounded line channel
// when a caller does not choose one. It is large enough that a bursty producer
// is not throttled line-by-line, small enough that a stalled consumer cannot
// grow an unbounded backlog.
const bashPPDefaultLineBuffer = 256

// bashPPStartLineProcess wraps an already-started source and begins streaming.
// buffer is the bounded channel capacity; values <= 0 select the default.
func bashPPStartLineProcess(ctx context.Context, src bashPPProcessSource, buffer int) *bashPPLineProcess {
	if buffer <= 0 {
		buffer = bashPPDefaultLineBuffer
	}
	pctx, cancel := context.WithCancel(ctx)
	p := &bashPPLineProcess{
		src:          src,
		ctx:          pctx,
		cancel:       cancel,
		lines:        make(chan string, buffer),
		producerDone: make(chan struct{}),
		stderrDone:   make(chan struct{}),
	}

	// Kill the process as soon as the context is cancelled, so a cancellation
	// unblocks the producer promptly rather than waiting on stdout EOF.
	go func() {
		<-pctx.Done()
		p.kill()
	}()

	if stderr := src.Stderr(); stderr != nil {
		go func() {
			// Drain stderr in full regardless of the consumer, so a child that
			// writes heavily to stderr never blocks on a full pipe.
			data, _ := io.ReadAll(stderr)
			p.mu.Lock()
			p.stderrBuf = data
			p.mu.Unlock()
			close(p.stderrDone)
		}()
	} else {
		close(p.stderrDone)
	}

	go p.produce()
	return p
}

// produce is the single writer of the lines channel and its sole closer.
func (p *bashPPLineProcess) produce() {
	defer close(p.lines)
	scanner := bufio.NewScanner(p.src.Stdout())
	// Allow arbitrarily long lines (up to a generous cap) rather than failing
	// with bufio.ErrTooLong on a line longer than the default 64KiB token.
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	defer close(p.producerDone)
	for scanner.Scan() {
		line := scanner.Text()
		select {
		case p.lines <- line:
		case <-p.ctx.Done():
			// Cancelled mid-stream: stop sending. Draining still happens in
			// Wait via Kill + reading stdout to EOF; the cancellation itself is
			// reported through Wait's error, not as a truncated data line.
			return
		}
	}
	if err := scanner.Err(); err != nil {
		p.mu.Lock()
		p.scanErr = err
		p.mu.Unlock()
		// A failed scanner no longer drains stdout. Terminate the source now:
		// otherwise a writer can remain blocked on its full pipe forever while
		// Wait waits for stderr EOF or process exit. Do not cancel p.ctx here;
		// the original scan error must remain distinct from cancellation.
		p.kill()
	}
}

// kill terminates the process at most once via the source.
func (p *bashPPLineProcess) kill() {
	p.killOnce.Do(p.src.Kill)
}

// Lines returns the receive-only bounded channel of stdout lines. It never
// blocks: it just hands back the channel. The channel is closed by the
// producer when the stream ends (EOF or cancellation); range over it and then
// call Wait for the exit status.
func (p *bashPPLineProcess) Lines() <-chan string { return p.lines }

// Wait blocks until the process has exited and been reaped, and reports its
// bash-style exit status. It is exact-once: the process is reaped only on the
// first call, and every call returns the identical (status, error).
//
// The error is non-nil only for a cancellation or an internal streaming
// failure — never for an ordinary non-zero exit, which is carried by status.
func (p *bashPPLineProcess) Wait() (int, error) {
	p.waitOnce.Do(func() {
		// Drain any lines the consumer left unread so the producer can reach
		// EOF and finish; this is what lets an early-stopping consumer still
		// reap the process instead of deadlocking it on a full pipe.
		for range p.lines {
		}
		<-p.producerDone
		<-p.stderrDone

		werr := p.src.Wait()
		p.status = bashPPWaitStatus(werr)

		p.mu.Lock()
		scanErr := p.scanErr
		p.mu.Unlock()

		switch {
		case werr != nil && p.status < 0:
			p.status = 1
			p.waitErr = werr
		case p.ctx.Err() != nil:
			// A cancelled stream reports the cancellation rather than whatever
			// exit the kill produced, mirroring the run/capture boundary.
			p.waitErr = p.ctx.Err()
		case scanErr != nil:
			p.waitErr = scanErr
		}

		// The process is reaped: release the cancel watcher goroutine. The
		// resulting kill is a no-op — the source has recorded completion, so it
		// will not signal an already-reaped (and possibly reused) pid.
		p.cancel()
	})
	return p.status, p.waitErr
}

// Close terminates the process (if still running), drains its streams and reaps
// it. It is safe to call after full consumption — it then just returns Wait's
// result — and safe to call more than once. Use it to abandon a live process
// early without leaking it.
func (p *bashPPLineProcess) Close() error {
	p.cancel()
	_, err := p.Wait()
	return err
}

// Stderr returns the process's fully drained standard error. It does not block
// once Wait has returned; before that it returns whatever has been drained so
// far. It is empty when the source did not capture stderr.
func (p *bashPPLineProcess) Stderr() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return string(p.stderrBuf)
}

// bashPPWaitStatus maps a source's Wait error to a bash-style status: an
// in-process subshell's bashPPStatusError carries its code directly; anything
// else goes through the platform's process mapping (bashPPProcessExitStatus).
func bashPPWaitStatus(err error) int {
	var se *bashPPStatusError
	if errors.As(err, &se) {
		return se.code
	}
	return bashPPProcessExitStatus(err)
}
