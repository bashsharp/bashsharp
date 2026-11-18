// Copyright (c) 2026, the outpost authors
// See LICENSE for licensing information

package interp

import (
	"strings"
	"testing"
)

// Sprint 245, story S245.8: `kill` is the only event that can tell the job
// table a Windows job stopped — NtSuspendProcess leaves the process
// suspended inside the exec goroutine's Wait, so no wait status ever
// arrives. The bookkeeping it drives is platform-neutral, so it is proven
// here on any host; the ntdll half is in suspend_windows_test.go.

// runningBg returns a live bgProc in the running state.
func runningBg(jobID int, cmd string) *bgProc {
	return &bgProc{
		done:  make(chan struct{}),
		exit:  new(exitStatus),
		cmd:   cmd,
		jobID: jobID,
	}
}

func mustSignal(t *testing.T, name string) killSig {
	t.Helper()
	sig, ok := signalByName(name)
	if !ok {
		t.Skipf("no %s in this platform's signal table", name)
	}
	return sig
}

// A stop recorded by kill must take the job out of `jobs -r`, put it into
// `jobs -s`, print "Stopped", and name the signal in POSIX mode — the exact
// sequence jobs.tests:166-181 checks after `kill -STOP %2`.
func TestRecordJobSignalStopsAndContinues(t *testing.T) {
	for _, name := range []string{"STOP", "TSTP", "TTIN", "TTOU"} {
		t.Run(name, func(t *testing.T) {
			r, buf := newJobFmtRunner(t, true)
			bg := runningBg(2, "sleep 350")
			jobs := []*bgProc{bg}

			r.recordJobSignal(bg, mustSignal(t, name))
			if !jobStoppedState(bg) {
				t.Fatal("kill -" + name + " left the job out of `jobs -s`")
			}
			if jobRunningState(bg) {
				t.Fatal("kill -" + name + " left the job listed by `jobs -r`")
			}
			if got, want := bg.getStopSignal(), "SIG"+name; got != want {
				t.Errorf("stop signal recorded as %q, want %q", got, want)
			}
			r.formatJob(jobs, bg, false, false)
			if got, want := stateWord(t, buf.String()), "Stopped(SIG"+name+")"; got != want {
				t.Errorf("jobs printed %q, want %q (line %q)", got, want, buf.String())
			}

			buf.Reset()
			r.recordJobSignal(bg, mustSignal(t, "CONT"))
			if jobStoppedState(bg) {
				t.Fatal("kill -CONT left the job listed by `jobs -s`")
			}
			if !jobRunningState(bg) {
				t.Fatal("kill -CONT left the job out of `jobs -r`")
			}
			if r.preferredJobID != bg.jobID {
				t.Errorf("continued job %d did not become the current job (%d)", bg.jobID, r.preferredJobID)
			}
			r.formatJob(jobs, bg, false, false)
			if got := stateWord(t, buf.String()); got != "Running" {
				t.Errorf("jobs printed %q after CONT, want Running (line %q)", got, buf.String())
			}
		})
	}
}

// A signal that neither stops nor continues must leave the state alone: a
// TERM'd job is still Running until its process actually dies.
func TestRecordJobSignalIgnoresOtherSignals(t *testing.T) {
	r, _ := newJobFmtRunner(t, false)
	bg := runningBg(1, "sleep 300")
	r.recordJobSignal(bg, mustSignal(t, "TERM"))
	if bg.jobState() != jobRunning || bg.getStopSignal() != "" {
		t.Fatalf("kill -TERM changed the job state: %v %q", bg.jobState(), bg.getStopSignal())
	}
	if r.preferredJobID != 0 {
		t.Fatalf("kill -TERM made job %d current", r.preferredJobID)
	}
}

// A stopped job becomes the current job and pushes the previously current
// one to `-`, which is what makes jobs.tests print "[2]+  Stopped" next to
// "[3]-  Running" after `kill -STOP %2`.
func TestStoppedJobBecomesCurrent(t *testing.T) {
	r, buf := newJobFmtRunner(t, false)
	one, two, three := runningBg(1, "sleep 300"), runningBg(2, "sleep 350"), runningBg(3, "sleep 400")
	jobs := []*bgProc{one, two, three}

	r.recordJobSignal(two, mustSignal(t, "STOP"))
	for _, bg := range jobs {
		r.formatJob(jobs, bg, false, false)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d job lines, want 3: %q", len(lines), buf.String())
	}
	for i, want := range []string{"[1] ", "[2]+", "[3]-"} {
		if !strings.HasPrefix(lines[i], want) {
			t.Errorf("job line %d is %q, want the %q marker", i+1, lines[i], want)
		}
	}
	if !strings.Contains(lines[1], "Stopped") || strings.Contains(lines[1], "&") {
		t.Errorf("stopped job line is %q, want a bare \"Stopped\" with no trailing &", lines[1])
	}
}
