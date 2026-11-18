// Copyright (c) 2026, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

//go:build windows

package interp

import (
	"context"
	"encoding/binary"
	"io"
	"os"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows has no poll(2), and (*os.File).SetReadDeadline works only for the
// handles the Go runtime poller accepts — which excludes both kinds this shell
// reads from: the console (CONIN$, what /dev/tty opens here) and the anonymous
// pipes behind pipelines, coprocs and `exec N<`. A blocking ReadFile on either
// cannot be called off, so `read -t` used to wait forever instead of timing
// out. The readiness probes below give the deadline something to sit on: the
// console handle is a waitable object, and a pipe answers PeekNamedPipe.

type windowsHandleKind int

const (
	// winHandleOther is a regular file or anything unrecognised: its read
	// completes without waiting for a peer, so it is always "ready".
	winHandleOther windowsHandleKind = iota
	winHandlePipe
	winHandleConsole
)

var (
	modkernel32           = windows.NewLazySystemDLL("kernel32.dll")
	procPeekNamedPipe     = modkernel32.NewProc("PeekNamedPipe")
	procPeekConsoleInputW = modkernel32.NewProc("PeekConsoleInputW")
)

// withHandle runs fn on the file's underlying handle. SyscallConn is used
// rather than Fd so a pollable file (should one ever reach here) is not
// detached from the runtime poller behind its owner's back.
func withHandle(f *os.File, fn func(windows.Handle)) bool {
	if f == nil {
		return false
	}
	raw, err := f.SyscallConn()
	if err != nil {
		return false
	}
	called := false
	if err := raw.Control(func(fd uintptr) {
		h := windows.Handle(fd)
		if h == windows.InvalidHandle {
			return
		}
		called = true
		fn(h)
	}); err != nil {
		return false
	}
	return called
}

func windowsHandleKindOf(f *os.File) windowsHandleKind {
	kind := winHandleOther
	withHandle(f, func(h windows.Handle) {
		t, err := windows.GetFileType(h)
		if err != nil {
			return
		}
		switch t {
		case windows.FILE_TYPE_PIPE:
			kind = winHandlePipe
		case windows.FILE_TYPE_CHAR:
			var mode uint32
			if windows.GetConsoleMode(h, &mode) == nil {
				kind = winHandleConsole
			}
		}
	})
	return kind
}

// pipeReadyNow reports whether a pipe has bytes buffered. A failed peek counts
// as ready: a broken pipe must reach the real read as EOF, and any other error
// is the read's to report.
func pipeReadyNow(h windows.Handle) bool {
	if err := procPeekNamedPipe.Find(); err != nil {
		return true
	}
	var avail uint32
	r1, _, _ := procPeekNamedPipe.Call(uintptr(h), 0, 0, 0,
		uintptr(unsafe.Pointer(&avail)), 0)
	if r1 == 0 {
		return true
	}
	return avail > 0
}

// A console handle is signalled by input records ReadFile never returns —
// mouse movement, focus changes, buffer resizes, key releases. Only a key
// press means a read can make progress, so the records are peeked (never
// consumed: the console is shared with whatever else reads it).
const winKeyEventRecord = 0x0001

// inputRecord mirrors INPUT_RECORD: a 16-bit EventType, two bytes of padding
// to the union's alignment, then the largest member (KEY_EVENT_RECORD, whose
// leading BOOL is bKeyDown).
type inputRecord struct {
	eventType uint16
	_         uint16
	event     [16]byte
}

func consoleKeyReadyNow(h windows.Handle) bool {
	if err := procPeekConsoleInputW.Find(); err != nil {
		return true
	}
	var count uint32
	if err := windows.GetNumberOfConsoleInputEvents(h, &count); err != nil {
		return true
	}
	if count == 0 {
		return false
	}
	var recs [32]inputRecord
	if count > uint32(len(recs)) {
		count = uint32(len(recs))
	}
	var read uint32
	r1, _, _ := procPeekConsoleInputW.Call(uintptr(h),
		uintptr(unsafe.Pointer(&recs[0])), uintptr(count),
		uintptr(unsafe.Pointer(&read)))
	if r1 == 0 {
		return true
	}
	for i := uint32(0); i < read; i++ {
		if recs[i].eventType != winKeyEventRecord {
			continue
		}
		if binary.LittleEndian.Uint32(recs[i].event[:4]) != 0 { // bKeyDown
			return true
		}
	}
	return false
}

// windowsReadReadyNow is a single non-blocking readiness probe, the Windows
// answer to a zero-timeout poll(2).
func windowsReadReadyNow(f *os.File) bool {
	ready := false
	switch windowsHandleKindOf(f) {
	case winHandlePipe:
		withHandle(f, func(h windows.Handle) { ready = pipeReadyNow(h) })
	case winHandleConsole:
		withHandle(f, func(h windows.Handle) { ready = consoleKeyReadyNow(h) })
	default:
		ready = true
	}
	return ready
}

func fdReadableNow(f *os.File) bool { return windowsReadReadyNow(f) }

func taskReadReadyNow(f *os.File) bool { return windowsReadReadyNow(f) }

// A pipe has no waitable object, so the only way to honour a deadline is to
// re-peek. Start tight, so a short timeout is not rounded up to a long one,
// then back off: an untimed read can sit here for as long as its peer is
// quiet, and must not spin while it does.
const (
	pipePollMin = time.Millisecond
	pipePollMax = 20 * time.Millisecond
)

// consoleSpinInterval keeps an already-signalled console handle (signalled by
// records a read would skip) from spinning while its deadline runs down.
const consoleSpinInterval = 5 * time.Millisecond

// waitReadyFor blocks up to d for the file to become readable.
func waitReadyFor(f *os.File, kind windowsHandleKind, d time.Duration) (bool, error) {
	if d < 0 {
		d = 0
	}
	start := time.Now()
	pause := pipePollMin
	for {
		switch kind {
		case winHandlePipe:
			ready := false
			if !withHandle(f, func(h windows.Handle) { ready = pipeReadyNow(h) }) {
				return true, nil
			}
			if ready {
				return true, nil
			}
		case winHandleConsole:
			// The console handle is waitable, so the deadline is the
			// kernel's to enforce rather than a polling loop's.
			remaining := d - time.Since(start)
			if remaining < 0 {
				remaining = 0
			}
			signalled := false
			ok := withHandle(f, func(h windows.Handle) {
				ev, err := windows.WaitForSingleObject(h, uint32(remaining.Milliseconds()))
				if err != nil {
					// Not a waitable handle after all; let the read decide.
					signalled = true
					return
				}
				switch ev {
				case uint32(windows.WAIT_OBJECT_0):
					signalled = consoleKeyReadyNow(h)
				case uint32(windows.WAIT_TIMEOUT):
				default:
					signalled = true
				}
			})
			if !ok || signalled {
				return true, nil
			}
		default:
			return true, nil
		}
		elapsed := time.Since(start)
		if elapsed >= d {
			return false, nil
		}
		if kind == winHandleConsole {
			pause = consoleSpinInterval
		}
		if rest := d - elapsed; rest < pause {
			pause = rest
		}
		time.Sleep(pause)
		if pause *= 2; pause > pipePollMax {
			pause = pipePollMax
		}
	}
}

// timeoutFileReader honours a read deadline on a handle the runtime poller
// will not take. Its zero deadline means "no timeout", used by callers that
// only need the context and the wake channel observed.
type timeoutFileReader struct {
	ctx      context.Context
	file     *os.File
	deadline time.Time
	wake     <-chan struct{}

	kind     windowsHandleKind
	kindOnce bool
}

func (r *timeoutFileReader) Read(p []byte) (int, error) {
	if r.file == nil {
		return 0, os.ErrInvalid
	}
	if !r.kindOnce {
		r.kind = windowsHandleKindOf(r.file)
		r.kindOnce = true
	}
	poller := &deadlinePoller{
		ctx:      r.ctx,
		deadline: r.deadline,
		wake:     r.wake,
		waitReady: func(d time.Duration) (bool, error) {
			return waitReadyFor(r.file, r.kind, d)
		},
	}
	if err := poller.waitReadable(); err != nil {
		return 0, err
	}
	return r.file.Read(p)
}

// timeoutReader wraps f in a reader that honours deadline even though
// SetReadDeadline does not work for the handle — which on Windows is the
// common case, not the exception.
func timeoutReader(ctx context.Context, f *os.File, deadline time.Time) io.Reader {
	if f == nil {
		return nil
	}
	return &timeoutFileReader{ctx: ctx, file: f, deadline: deadline}
}

// signalReader has no Windows implementation: interrupting an untimed read
// would mean polling a pipe for as long as the read lasts. Returning nil keeps
// the plain blocking read.
func signalReader(context.Context, *os.File, <-chan struct{}) io.Reader { return nil }

// taskReadReader has no Windows implementation; a Bash++ task reports
// blocking input as unavailable rather than stranding its group.
func taskReadReader(context.Context, *os.File, time.Time, <-chan struct{}, func() bool) io.Reader {
	return nil
}

// cancellableReader wraps a read whose blocking cannot be called off any other
// way. An untimed read from a pipe or the console — `read LINE <&${COPROC[0]}`
// waiting on a coprocess, a `read` on the far side of a pipeline — is issued
// as a plain ReadFile that no SetReadDeadline reaches, so cancelling the
// runner's context left the shell wedged in it for good. Returning nil means
// the caller's own SetReadDeadline is enough.
func cancellableReader(ctx context.Context, f *os.File) io.Reader {
	if f == nil || ctx == nil || ctx.Done() == nil {
		return nil
	}
	// The kind is the whole test: Windows never hands an os.Pipe end to the
	// runtime poller, so SetReadDeadline is already known to have failed for
	// it — and to have been unavailable to any caller upstream, which is why
	// none of them can have armed a deadline we would be dropping here.
	//
	// Only pipes. A regular file does not wait on a peer, and a console read
	// is left alone deliberately: its line editing is the console host's, and
	// a `read` with no timeout waiting for the user to press enter is what
	// bash does too. A console read that must give up on time goes through
	// timeoutFileReader with a deadline instead.
	if windowsHandleKindOf(f) == winHandlePipe {
		return &timeoutFileReader{ctx: ctx, file: f}
	}
	return nil
}
