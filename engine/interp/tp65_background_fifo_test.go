// Copyright (c) 2026, the bashy authors
// See LICENSE for licensing information

//go:build unix

package interp_test

import (
	"testing"

	"github.com/bashsharp/bashsharp/engine/interp"
	"golang.org/x/sys/unix"
)

// TestBackgroundExternalLaunchFIFORedirectDoesNotDeadlock covers the TP65
// reducer:
//
//	mkfifo f; cat <f >/dev/null & printf x >f; wait
//
// The external-launch handoff (added so a job-carrier-backed shell gives an
// asynchronous external command the same practical head start a real
// fork() gives it) used to rendezvous on the background job's real OS PID.
// That PID is only assigned once the job's own redirection setup finishes —
// but here the job's redirection setup is `cat`'s blocking open of the FIFO
// for reading, which cannot finish until the foreground opens the write
// side. Waiting on the PID therefore wedged the foreground behind a launch
// that was itself waiting on the foreground. The fix rendezvous on the
// background goroutine having merely started, not on the real launch
// completing, so the foreground is always free to reach the write side.
func TestBackgroundExternalLaunchFIFORedirectDoesNotDeadlock(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := unix.Mkfifo(dir+"/f", 0o600); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}

	out := runCarrierScript(t, new(testCarrier), `
cat <f >/dev/null &
printf x >f
wait
echo done`, interp.Dir(dir))
	if want := "done\n"; out != want {
		t.Fatalf("output = %q, want %q", out, want)
	}
}
