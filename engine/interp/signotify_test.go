// Copyright (c) 2026, the outpost authors
// See LICENSE for licensing information

//go:build unix

package interp

import (
	"bytes"
	"context"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

type readySignalWriter struct {
	buf   bytes.Buffer
	ready chan struct{}
	trap  chan struct{}
}

func (w *readySignalWriter) Write(p []byte) (int, error) {
	n, err := w.buf.Write(p)
	if strings.Contains(w.buf.String(), "ready\n") {
		select {
		case <-w.ready:
		default:
			close(w.ready)
		}
	}
	if strings.Contains(w.buf.String(), "USR1 received\n") {
		select {
		case <-w.trap:
		default:
			close(w.trap)
		}
	}
	return n, err
}

// fabricateWaitStatus builds a syscall.WaitStatus reporting a termination by
// sig, with the core-dump bit optionally set. Both Linux and the BSDs (macOS)
// encode the terminating signal in the low 7 bits and the core flag in 0x80,
// so this is portable across the `unix` build tag.
func fabricateWaitStatus(sig syscall.Signal, core bool) waitStatus {
	v := int(sig)
	if core {
		v |= 0x80
	}
	return syscall.WaitStatus(v)
}

func TestReadInterruptedByTrappedSignal(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pr.Close()
	defer pw.Close()

	file, err := syntax.NewParser().Parse(strings.NewReader(
		"trap 'echo USR1 received' USR1\n"+
			"echo ready\n"+
			"read x\n"+
			"echo got:$x\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	out := &readySignalWriter{ready: make(chan struct{}), trap: make(chan struct{})}
	r, err := New(StdIO(pr, out, out))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	errc := make(chan error, 1)
	done := make(chan struct{})
	t.Cleanup(func() {
		cancel()
		_ = pr.Close()
		_ = pw.Close()
		<-done
		r.Reset()
	})
	go func() {
		defer close(done)
		errc <- r.Run(ctx, file)
	}()
	select {
	case <-out.ready:
	case <-time.After(2 * time.Second):
		t.Fatalf("shell did not reach read; output so far: %q", out.buf.String())
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGUSR1); err != nil {
		t.Fatal(err)
	}
	select {
	case <-out.trap:
	case <-time.After(2 * time.Second):
		t.Fatalf("shell did not run USR1 trap; output so far: %q", out.buf.String())
	}
	if _, err := pw.WriteString("input\n"); err != nil {
		t.Fatal(err)
	}
	pw.Close()
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	r.sigMu.Lock()
	sub, subscribed := r.sigNotifyCh["USR1"]
	r.sigMu.Unlock()
	if !subscribed {
		t.Fatal("USR1 trap subscription missing before owner cleanup")
	}
	// Run preserves traps for incremental use; this fixture owns the Runner
	// and must explicitly join its subscription when that use is finished.
	r.Reset()
	select {
	case <-sub.finished:
	default:
		t.Error("read fixture left its signal subscription running")
	}
	if got, want := out.buf.String(), "ready\nUSR1 received\ngot:input\n"; got != want {
		t.Fatalf("output mismatch\n got: %q\nwant: %q", got, want)
	}
}

// TestForegroundSignalDeathNotify covers #25/#26: a foreground external
// command killed by a fatal signal in a non-interactive shell prints a
// bash-style status line in default mode and prints nothing in POSIX mode.
// It exercises notifyForegroundSignalDeath directly with a fabricated
// WaitStatus so the assertions never depend on a real core dump (which the
// test host's `ulimit -c` would otherwise gate).
func TestForegroundSignalDeathNotify(t *testing.T) {
	t.Parallel()

	// Sanity-check the fabricated statuses behave like real ones.
	if st := fabricateWaitStatus(syscall.SIGSEGV, true); !st.Signaled() || !st.CoreDump() {
		t.Fatalf("fabricated SIGSEGV+core: Signaled=%v CoreDump=%v, want both true",
			st.Signaled(), st.CoreDump())
	}
	if st := fabricateWaitStatus(syscall.SIGTERM, false); !st.Signaled() || st.CoreDump() {
		t.Fatalf("fabricated SIGTERM: Signaled=%v CoreDump=%v, want true/false",
			st.Signaled(), st.CoreDump())
	}

	const pid = 12345
	args := []string{"sh", "-c", "kill -s SIG $$"}
	const cmd = "sh -c kill -s SIG $$"

	tests := []struct {
		name        string
		sig         syscall.Signal
		core        bool
		posix       bool
		interactive bool
		want        string
	}{
		{
			name: "segv default core",
			sig:  syscall.SIGSEGV, core: true,
			want: "bash: line 0: 12345 Segmentation fault (core dumped) " + cmd + "\n",
		},
		{
			name: "abrt default core",
			sig:  syscall.SIGABRT, core: true,
			want: "bash: line 0: 12345 Aborted (core dumped) " + cmd + "\n",
		},
		{
			name: "fpe default core uses arithmetic exception",
			sig:  syscall.SIGFPE, core: true,
			want: "bash: line 0: 12345 Arithmetic exception (core dumped) " + cmd + "\n",
		},
		{
			name: "term default bare",
			sig:  syscall.SIGTERM, core: false,
			want: "Terminated " + cmd + "\n",
		},
		{
			name: "int suppressed",
			sig:  syscall.SIGINT, core: false,
			want: "",
		},
		{
			name: "pipe suppressed",
			sig:  syscall.SIGPIPE, core: false,
			want: "",
		},
		{
			name: "segv posix suppressed",
			sig:  syscall.SIGSEGV, core: true, posix: true,
			want: "",
		},
		{
			name: "term posix suppressed",
			sig:  syscall.SIGTERM, core: false, posix: true,
			want: "",
		},
		{
			name: "segv interactive suppressed",
			sig:  syscall.SIGSEGV, core: true, interactive: true,
			want: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := &Runner{}
			r.bashCompatErrors = true
			r.filename = "bash"
			r.opts[optPosix] = tc.posix
			r.interactiveShell = tc.interactive

			var buf bytes.Buffer
			r.notifyForegroundSignalDeath(&buf, syntax.Pos{}, pid,
				fabricateWaitStatus(tc.sig, tc.core), args)

			if got := buf.String(); got != tc.want {
				t.Fatalf("notifyForegroundSignalDeath output mismatch\n got: %q\nwant: %q", got, tc.want)
			}
		})
	}
}
