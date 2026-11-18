// Copyright (c) 2026, the bashy authors
// See LICENSE for licensing information

//go:build !unix

package interp

import (
	"strings"
	"testing"
)

func TestFgRealProcessGroupFailsClosedOffUnix(t *testing.T) {
	job := stoppedBg(1, "worker", "")
	job.jobControl = true
	job.pid.Store(4242)
	job.pidReady = make(chan struct{})
	close(job.pidReady)
	got := runIssue7JobCommand(t, "fg %1", job)
	if got.status == 0 || !strings.Contains(got.stderr, "unsupported") {
		t.Fatalf("non-Unix fg result: %#v", got)
	}
}
