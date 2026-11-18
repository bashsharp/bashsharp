// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestBashPPProcessOversizeLineReaps(t *testing.T) {
	pr, pw := io.Pipe()
	src := &fakeProcessSource{
		stdout:  pr,
		release: make(chan struct{}),
		killFn:  func() { _ = pw.Close() },
	}
	p := bashPPStartLineProcess(context.Background(), src, 1)
	written := make(chan struct{})
	go func() {
		defer close(written)
		_, _ = io.WriteString(pw, strings.Repeat("x", 17*1024*1024))
	}()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, err := p.Wait()
		if !errors.Is(err, bufio.ErrTooLong) {
			t.Errorf("Wait error = %v, want scanner token-too-long", err)
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		p.kill()
		t.Fatal("scanner failure did not terminate and reap the blocked writer")
	}
	<-written
	if !src.killed.Load() || src.waits.Load() != 1 {
		t.Fatalf("killed=%v waits=%d", src.killed.Load(), src.waits.Load())
	}
}

func TestBashPPRunLinesOversizeCapture(t *testing.T) {
	line := strings.Repeat("x", 17*1024*1024)
	got := bashPPSplitLines(line + "\nlast\n")
	if len(got) != 2 || got[0] != line || got[1] != "last" {
		t.Fatal("completed capture Lines silently truncated a long line")
	}
}

// Tests for the one internal bounded Go-channel process line substrate
// (bashPPLineProcess) that underpins B14's live `start(...)` surface.
//
// PROVENANCE — the process-lifecycle assertions below are faithfully ported,
// with their original intent preserved, from two upstream test corpora:
//
//   - Project:  The Go programming language, standard library `os/exec`.
//     Pinned:   go1.27.1 (GOROOT sdk go1.27.1), src/os/exec/exec_test.go.
//     License:  BSD-3-Clause ($GOROOT/LICENSE, "Copyright (c) 2009 The Go
//               Authors").
//     Ported:   TestExitStatus / TestExitCode (exit-code passthrough) ->
//               TestBashPPProcessExitStatusFidelity; TestContext /
//               TestContextCancel (cancellation kills and Wait then fails, the
//               process does not outlive the context) ->
//               TestBashPPProcessContextCancel; the double-Wait / reaping
//               invariant behind TestPipes/TestDoubleStart -> the exact-once
//               Wait tests. The upstream tests drive a compiled `exec` test
//               helper binary via helperCommand; we cannot ship that helper, so
//               the ADAPTATION is to drive the host `sh` with the same observable
//               behaviours (exit N, sleep, write to two streams). Original
//               assertions — exit code equals N, a cancelled command's Wait
//               returns an error and the process is gone, output arrives whole —
//               are preserved.
//
//   - Project:  GNU Bash, the reference shell whose wait/status semantics this
//     dialect matches.
//     Spec:     Bash Reference Manual, "Exit Status" — "When a command
//               terminates on a fatal signal whose number is N, Bash uses the
//               value 128+N as the exit status"; the `wait` builtin returns the
//               exit status of the awaited process. (Bash 5.3; the same rule the
//               repo's TestRunnerRunConfirm oracle pins against real bash.)
//     Ported:   the 128+signal exit-status rule and exit-code passthrough ->
//               TestBashPPProcessSignalStatus (a SIGTERM'd child reports 143)
//               and TestBashPPProcessExitStatusFidelity.
//     Adaptation: expressed against bashPPProcessExitStatus / the substrate's
//               Wait rather than a `$?` string, since this is a Go-level unit.
//
// The line-semantics, backpressure, early-close and leak tests below use an
// in-memory fake source so they run identically on every OS, including where no
// real `sh` is present (Windows CI); the real-process ports skip when `sh` is
// unavailable and the signal rule is unix-only, matching the run/capture
// test-suite conventions in this package.

// --- an in-memory source, for OS-independent semantics ---

// fakeProcessSource is a bashPPProcessSource backed by in-memory readers, with
// a controllable Wait and a Kill that unblocks a blocked stdout reader.
type fakeProcessSource struct {
	stdout  io.Reader
	stderr  io.Reader
	waitErr error

	release  chan struct{} // Wait returns once this is closed (or on Kill)
	killFn   func()        // e.g. close a pipe writer to EOF a blocked reader
	killOnce sync.Once
	killed   atomic.Bool
	waits    atomic.Int32
}

func (f *fakeProcessSource) Stdout() io.Reader { return f.stdout }
func (f *fakeProcessSource) Stderr() io.Reader { return f.stderr }

func (f *fakeProcessSource) Wait() error {
	f.waits.Add(1)
	if f.release != nil {
		<-f.release
	}
	return f.waitErr
}

func (f *fakeProcessSource) Kill() {
	f.killOnce.Do(func() {
		f.killed.Store(true)
		if f.killFn != nil {
			f.killFn()
		}
		if f.release != nil {
			close(f.release)
		}
	})
}

// completedSource returns a source whose Wait returns immediately.
func completedSource(stdout, stderr string, waitErr error) *fakeProcessSource {
	return &fakeProcessSource{
		stdout:  strings.NewReader(stdout),
		stderr:  strings.NewReader(stderr),
		waitErr: waitErr,
	}
}

func collectLines(p *bashPPLineProcess) []string {
	var got []string
	for line := range p.Lines() {
		got = append(got, line)
	}
	return got
}

func TestBashPPProcessLineSemantics(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, stdout string
		want         []string
	}{
		{"empty", "", nil},
		{"single terminated", "one\n", []string{"one"}},
		{"final unterminated", "one\ntwo", []string{"one", "two"}},
		{"empty middle line", "a\n\nb\n", []string{"a", "", "b"}},
		{"empty final line then eof", "a\n\n", []string{"a", ""}},
		{"only newline", "\n", []string{""}},
		{"trailing crlf stripped", "a\r\nb\r\n", []string{"a", "b"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			src := completedSource(tc.stdout, "", nil)
			p := bashPPStartLineProcess(context.Background(), src, 4)
			got := collectLines(p)
			if fmt.Sprint(got) != fmt.Sprint(tc.want) {
				t.Fatalf("lines = %q, want %q", got, tc.want)
			}
			status, err := p.Wait()
			if status != 0 || err != nil {
				t.Fatalf("Wait = (%d, %v), want (0, nil)", status, err)
			}
		})
	}
}

func TestBashPPProcessLargeStdoutStderr(t *testing.T) {
	t.Parallel()
	// Both streams exceed a pipe buffer; the substrate must drain both without
	// deadlock even though only stdout is consumed as lines.
	const nLines = 5000
	var sb strings.Builder
	for i := 0; i < nLines; i++ {
		fmt.Fprintf(&sb, "line-%05d-%s\n", i, strings.Repeat("x", 32))
	}
	bigOut := sb.String()
	bigErr := strings.Repeat("E", 200*1024) // 200KiB of stderr, never as lines
	if len(bigOut) <= 64*1024 {
		t.Fatalf("stdout fixture too small: %d bytes", len(bigOut))
	}

	src := completedSource(bigOut, bigErr, nil)
	p := bashPPStartLineProcess(context.Background(), src, 8)
	got := collectLines(p)
	if len(got) != nLines {
		t.Fatalf("got %d lines, want %d: a large stream must arrive whole", len(got), nLines)
	}
	for i, line := range got {
		if want := fmt.Sprintf("line-%05d-%s", i, strings.Repeat("x", 32)); line != want {
			t.Fatalf("line %d = %q, want %q", i, line, want)
		}
	}
	if _, err := p.Wait(); err != nil {
		t.Fatalf("Wait err = %v, want nil", err)
	}
	if got := p.Stderr(); len(got) != len(bigErr) {
		t.Fatalf("stderr drained %d bytes, want %d", len(got), len(bigErr))
	}
}

// A slow consumer applies backpressure to a bounded channel; every line still
// arrives, in order. The channel capacity is exactly the one requested.
func TestBashPPProcessBoundedBackpressure(t *testing.T) {
	t.Parallel()
	const nLines = 200
	var sb strings.Builder
	for i := 0; i < nLines; i++ {
		fmt.Fprintf(&sb, "%d\n", i)
	}
	src := completedSource(sb.String(), "", nil)
	p := bashPPStartLineProcess(context.Background(), src, 2)
	if got := cap(p.Lines()); got != 2 {
		t.Fatalf("channel capacity = %d, want the bounded 2", got)
	}
	var got []string
	for line := range p.Lines() {
		time.Sleep(time.Millisecond) // stall so the buffer fills and backpressures
		got = append(got, line)
	}
	if len(got) != nLines {
		t.Fatalf("got %d lines under backpressure, want %d", len(got), nLines)
	}
	for i, line := range got {
		if line != fmt.Sprint(i) {
			t.Fatalf("out-of-order at %d: %q", i, line)
		}
	}
	if _, err := p.Wait(); err != nil {
		t.Fatalf("Wait err = %v", err)
	}
}

// Wait is exact-once: the source is reaped a single time and every call returns
// the identical (status, error). Ported from the os/exec reaping invariant
// (Wait must be called once per process).
func TestBashPPProcessExactOnceWait(t *testing.T) {
	t.Parallel()
	src := completedSource("a\nb\n", "", &exec.ExitError{}) // any non-nil wait error
	src.waitErr = nil                                       // keep status 0; exercise repeat calls
	p := bashPPStartLineProcess(context.Background(), src, 4)
	_ = collectLines(p)

	first, ferr := p.Wait()
	for i := 0; i < 5; i++ {
		s, e := p.Wait()
		if s != first || e != ferr {
			t.Fatalf("Wait call %d = (%d, %v), want the stable (%d, %v)", i, s, e, first, ferr)
		}
	}
	if got := src.waits.Load(); got != 1 {
		t.Fatalf("source Wait called %d times, want exactly once", got)
	}
}

// A consumer that stops reading early must still leave the process drained and
// reaped, not leaked. Close cancels, drains and reaps.
func TestBashPPProcessEarlyCloseReaps(t *testing.T) {
	t.Parallel()
	pr, pw := io.Pipe()
	src := &fakeProcessSource{
		stdout:  pr,
		stderr:  strings.NewReader(""),
		release: make(chan struct{}),
		killFn:  func() { _ = pw.Close() },
	}
	p := bashPPStartLineProcess(context.Background(), src, 1)

	// A goroutine keeps trying to produce; the consumer reads only one line.
	go func() {
		for i := 0; ; i++ {
			if _, err := fmt.Fprintf(pw, "line-%d\n", i); err != nil {
				return
			}
		}
	}()
	if line, ok := <-p.Lines(); !ok || !strings.HasPrefix(line, "line-") {
		t.Fatalf("first line = %q, ok=%v", line, ok)
	}

	done := make(chan error, 1)
	go func() { done <- p.Close() }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Close did not return: an early-stopping consumer leaked the process")
	}
	if !src.killed.Load() {
		t.Fatal("early close did not terminate the process")
	}
	if _, ok := <-p.Lines(); ok {
		t.Fatal("lines channel still open after Close: producer was not the sole closer")
	}
}

// Cancelling the context terminates the process, closes the line channel, and
// Wait reports the cancellation as an error separate from the exit status.
// Ported from os/exec TestContext / TestContextCancel.
func TestBashPPProcessContextCancelFake(t *testing.T) {
	t.Parallel()
	pr, pw := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	src := &fakeProcessSource{
		stdout:  pr,
		stderr:  strings.NewReader(""),
		release: make(chan struct{}),
		killFn:  func() { _ = pw.Close() },
	}
	p := bashPPStartLineProcess(ctx, src, 4)
	go func() { _, _ = fmt.Fprint(pw, "before-cancel\n") }()
	if line := <-p.Lines(); line != "before-cancel" {
		t.Fatalf("first line = %q", line)
	}

	cancel()
	// The channel must close (producer is the sole closer) and Wait must report
	// the cancellation, not a truncated success.
	for range p.Lines() {
	}
	status, err := p.Wait()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait err = %v, want context.Canceled", err)
	}
	_ = status
	if !src.killed.Load() {
		t.Fatal("cancellation did not terminate the process")
	}
}

// After a run completes, the cancel-watcher goroutine and the producer/stderr
// goroutines must all exit — a process that finished normally must not leak.
func TestBashPPProcessNoGoroutineLeak(t *testing.T) {
	// Process-global goroutine accounting must run before parallel tests;
	// otherwise unrelated live workers are counted as substrate leaks.
	settle := func() int {
		var n int
		for i := 0; i < 50; i++ {
			runtime.GC()
			n = runtime.NumGoroutine()
			time.Sleep(2 * time.Millisecond)
		}
		return n
	}
	before := settle()
	for i := 0; i < 50; i++ {
		src := completedSource("a\nb\nc\n", "err\n", nil)
		p := bashPPStartLineProcess(context.Background(), src, 4)
		_ = collectLines(p)
		if _, err := p.Wait(); err != nil {
			t.Fatalf("Wait err = %v", err)
		}
	}
	after := settle()
	if after > before+3 {
		t.Fatalf("goroutines grew from %d to %d: the substrate leaked", before, after)
	}
}

// --- ports over a real process ---

func skipNoShell(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("no POSIX sh; the in-memory fakes cover the substrate logic on windows")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skipf("sh not found: %v", err)
	}
}

// Ported from os/exec TestExitStatus / TestExitCode: exit codes pass through
// unchanged, and a signal-free normal exit yields exactly that code.
func TestBashPPProcessExitStatusFidelity(t *testing.T) {
	skipNoShell(t)
	t.Parallel()
	for _, code := range []int{0, 1, 42, 255} {
		t.Run(fmt.Sprintf("exit-%d", code), func(t *testing.T) {
			t.Parallel()
			cmd := exec.Command("sh", "-c", fmt.Sprintf("echo out; exit %d", code))
			p, err := bashPPStartCmd(context.Background(), cmd, 4)
			if err != nil {
				t.Fatal(err)
			}
			lines := collectLines(p)
			if len(lines) != 1 || lines[0] != "out" {
				t.Fatalf("lines = %q, want [out]", lines)
			}
			status, werr := p.Wait()
			if werr != nil {
				t.Fatalf("Wait err = %v, want nil: a non-zero exit is a status, not an error", werr)
			}
			if status != code {
				t.Fatalf("status = %d, want %d", status, code)
			}
		})
	}
}

// Ported from the Bash Reference Manual "Exit Status" 128+N rule: a child
// terminated by SIGTERM (15) reports 143. Unix-only, as the signal encoding is.
func TestBashPPProcessSignalStatus(t *testing.T) {
	skipNoShell(t)
	if runtime.GOOS == "plan9" {
		t.Skip("plan9 has no unix signal-exit encoding")
	}
	t.Parallel()
	cmd := exec.Command("sh", "-c", "kill -TERM $$")
	p, err := bashPPStartCmd(context.Background(), cmd, 4)
	if err != nil {
		t.Fatal(err)
	}
	_ = collectLines(p)
	status, werr := p.Wait()
	if werr != nil {
		t.Fatalf("Wait err = %v, want nil", werr)
	}
	if status != 143 {
		t.Fatalf("status = %d, want 143 (128+SIGTERM), the bash convention", status)
	}
}

// Ported from os/exec TestContext / TestContextCancel: cancelling stops the
// process promptly, Wait then reports the cancellation, and the process does
// not outlive the context.
func TestBashPPProcessContextCancel(t *testing.T) {
	skipNoShell(t)
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	// A child that prints one line then blocks forever reading stdin.
	cmd := exec.Command("sh", "-c", "echo ready; cat")
	p, err := bashPPStartCmd(ctx, cmd, 4)
	if err != nil {
		t.Fatal(err)
	}
	if line := <-p.Lines(); line != "ready" {
		t.Fatalf("first line = %q, want ready", line)
	}

	cancel()
	done := make(chan struct{})
	var status int
	var werr error
	go func() {
		status, werr = p.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("Wait did not return after cancel: the process outlived its context")
	}
	if !errors.Is(werr, context.Canceled) {
		t.Fatalf("Wait err = %v, want context.Canceled", werr)
	}
	_ = status
}

// A second Wait on a real, fully consumed process returns the same status and
// does not attempt a second reap (which would error on a real os.Process).
func TestBashPPProcessRealDoubleWait(t *testing.T) {
	skipNoShell(t)
	t.Parallel()
	cmd := exec.Command("sh", "-c", "printf 'a\\nb\\n'; exit 7")
	p, err := bashPPStartCmd(context.Background(), cmd, 4)
	if err != nil {
		t.Fatal(err)
	}
	if got := collectLines(p); len(got) != 2 {
		t.Fatalf("lines = %q, want two", got)
	}
	s1, e1 := p.Wait()
	s2, e2 := p.Wait()
	if s1 != 7 || s2 != 7 || e1 != nil || e2 != nil {
		t.Fatalf("double Wait = (%d,%v),(%d,%v), want stable (7,nil)", s1, e1, s2, e2)
	}
}
