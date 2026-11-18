// Copyright (c) 2026, the bashy authors
// See LICENSE for licensing information

//go:build !plan9 && !js

package interactive

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-quicktest/qt"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer // bashpp-racegate:safe-synchronized
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

// TestBackgroundJobSurvivesStatementBoundary pins the VSC ps/SIGTTIN
// conformance hang (Bashy issue: an interactive `ps.ex` test set wedges with
// a job carrier alive and a child stopped in T/do_signal_stop forever, never
// resuming). Root cause: this package used to run each statement under its
// own context.WithCancel(ctx), cancelled immediately after Run returned —
// which for `cmd &` is as soon as the job is launched, not when it finishes.
// context.WithCancel propagates to every descendant context, including a
// background job's own (see interp.Runner.stmt), so that immediate cancel
// tore the job down before — or while — it was still running. No trap,
// signal, or explicit kill was involved: an interactive `sleep &` died on
// the spot, and a job whose child had legitimately stopped for an external
// signal lost its carrier and its represented job to a synthetic "context
// canceled" kill instead of surviving to be resumed.
//
// This test backgrounds a job on one input line and waits for it on the
// next — two separate statements, so any reintroduced per-statement
// cancellation between them fails it exactly the way the wedge did.
func TestBackgroundJobSurvivesStatementBoundary(t *testing.T) {
	input := strings.NewReader("sleep 0.1; echo done &\nwait $!\necho status=$?\nexit 0\n")
	var stdout, stderr lockedBuffer
	r, err := interp.New(
		interp.Interactive(true),
		interp.StdIO(strings.NewReader(""), &stdout, &stderr),
	)
	qt.Assert(t, qt.IsNil(err))

	runDone := make(chan error, 1)
	go func() {
		runDone <- runFallback(context.Background(), r, input, &stdout, &stderr,
			syntax.LangBash, false,
			func() string { return "PROMPT> " },
			func() string { return "> " },
			func(error) {}, nil, nil, nil)
	}()

	select {
	case err := <-runDone:
		qt.Assert(t, qt.IsNil(err))
	case <-time.After(5 * time.Second):
		t.Fatalf("interactive loop did not finish; output so far:\nstdout=%q\nstderr=%q", stdout.String(), stderr.String())
	}

	qt.Check(t, qt.StringContains(stdout.String(), "done"),
		qt.Commentf("backgrounded job never ran to completion (its launch context was cancelled out from under it at the statement boundary); stdout=%q stderr=%q", stdout.String(), stderr.String()))
	qt.Check(t, qt.StringContains(stdout.String(), "status=0"),
		qt.Commentf("wait on the background job did not see a normal exit; stdout=%q stderr=%q", stdout.String(), stderr.String()))
}
