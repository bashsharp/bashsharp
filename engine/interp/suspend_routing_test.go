// Copyright (c) 2026, the outpost authors
// See LICENSE for licensing information

package interp

import "testing"

// Sprint 245, story S245.8: which Windows signal suspends, resumes,
// terminates or merely probes a process is decided by signal_wintable.go, so
// it is proven here on any host. suspend_windows_test.go then exercises the
// ntdll calls the routing selects on a Windows worker.

func TestWindowsSignalActionFor(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		want windowsSignalAction
	}{
		{"STOP", windowsActionSuspend},
		{"TSTP", windowsActionSuspend},
		{"TTIN", windowsActionSuspend},
		{"TTOU", windowsActionSuspend},
		{"CONT", windowsActionResume},
		{"CHLD", windowsActionProbe},
		{"URG", windowsActionProbe},
		{"WINCH", windowsActionProbe},
		{"TERM", windowsActionTerminate},
		{"KILL", windowsActionTerminate},
		{"HUP", windowsActionTerminate},
		{"USR1", windowsActionTerminate},
	} {
		e, ok := windowsSignalByName(tc.name)
		if !ok {
			t.Fatalf("no %s in the Windows signal table", tc.name)
		}
		if got := windowsSignalActionFor(e.Num); got != tc.want {
			t.Errorf("windowsSignalActionFor(%s=%d) = %d, want %d", tc.name, e.Num, got, tc.want)
		}
	}
}

// Every signal in the table must land on exactly one action, and only the
// job-control set may suspend or resume: a typo in the number constants
// would otherwise silently turn some unrelated signal into a stop.
func TestWindowsSignalActionTotality(t *testing.T) {
	t.Parallel()
	suspends, resumes := 0, 0
	for _, e := range windowsSignalTable() {
		switch windowsSignalActionFor(e.Num) {
		case windowsActionSuspend:
			suspends++
			if !windowsSignalStopsJob(e.Num) {
				t.Errorf("%s suspends but is not in the stop set", e.Name)
			}
		case windowsActionResume:
			resumes++
			if e.Name != "CONT" {
				t.Errorf("%s resumes but is not CONT", e.Name)
			}
		case windowsActionProbe:
			if !windowsSignalDefaultDoesNotTerminate(e.Num) {
				t.Errorf("%s probes but its default action is death", e.Name)
			}
		case windowsActionTerminate:
			if windowsSignalDefaultDoesNotTerminate(e.Num) {
				t.Errorf("%s terminates but its default action is not death", e.Name)
			}
		default:
			t.Errorf("%s has no action", e.Name)
		}
	}
	if suspends != 4 {
		t.Errorf("%d signals suspend, want 4 (STOP, TSTP, TTIN, TTOU)", suspends)
	}
	if resumes != 1 {
		t.Errorf("%d signals resume, want 1 (CONT)", resumes)
	}
}

// The pipe hop must be skipped for exactly KILL and the job-control set: a
// stop offered to a sibling bashy would be taken as an untrapped signal and
// exit it with the marker, and a CONT could never be written to a pipe
// served by the very process it has to wake up.
func TestWindowsSignalBypassesPipe(t *testing.T) {
	t.Parallel()
	want := map[string]bool{"KILL": true, "STOP": true, "TSTP": true, "TTIN": true, "TTOU": true, "CONT": true}
	for _, e := range windowsSignalTable() {
		if got := windowsSignalBypassesPipe(e.Num); got != want[e.Name] {
			t.Errorf("windowsSignalBypassesPipe(%s=%d) = %v, want %v", e.Name, e.Num, got, want[e.Name])
		}
	}
}
