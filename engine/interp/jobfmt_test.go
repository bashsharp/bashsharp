// Copyright (c) 2026, the bashy authors
// See LICENSE for licensing information

package interp

import (
	"bytes"
	"strings"
	"testing"
)

// newJobFmtRunner builds a minimal Runner whose stdout is the returned
// buffer, for exercising the job-state formatting helpers directly.
func newJobFmtRunner(t *testing.T, posix bool) (*Runner, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	r, err := New(StdIO(nil, &buf, &buf))
	if err != nil {
		t.Fatal(err)
	}
	r.Reset()
	r.stdout = &buf
	r.opts[optPosix] = posix
	return r, &buf
}

// doneBg returns a finished bgProc with the given exit code.
func doneBg(jobID int, cmd string, code uint8) *bgProc {
	done := make(chan struct{})
	close(done)
	bg := &bgProc{
		done:  done,
		exit:  &exitStatus{code: code},
		cmd:   cmd,
		jobID: jobID,
	}
	bg.setState(jobDead)
	return bg
}

// stoppedBg returns a stopped bgProc carrying the given SIG-prefixed stop
// signal name. Its done channel stays open so jobStoppedState sees it.
func stoppedBg(jobID int, cmd, stopSig string) *bgProc {
	bg := &bgProc{
		done:  make(chan struct{}),
		exit:  new(exitStatus),
		cmd:   cmd,
		jobID: jobID,
	}
	bg.setState(jobStopped)
	if stopSig != "" {
		bg.setStopSignal(stopSig)
	}
	return bg
}

// stateWord extracts the job-state column from a formatJob line of the
// form "[1]+  <State padded><cmd>...". Returns the run of non-padding
// text after the marker, up to the first run of two or more spaces.
func stateWord(t *testing.T, line string) string {
	t.Helper()
	// Drop the "[id]marker  " prefix: everything up to and including the
	// double space after the marker.
	i := strings.Index(line, "  ")
	if i < 0 {
		t.Fatalf("no marker/state separator in %q", line)
	}
	rest := strings.TrimLeft(line[i:], " ")
	// State word ends at the next run of 2+ spaces (the padding to the cmd).
	if j := strings.Index(rest, "  "); j >= 0 {
		return rest[:j]
	}
	return strings.TrimRight(rest, "\n")
}

func TestFormatJobState(t *testing.T) {
	tests := []struct {
		name  string
		posix bool
		bg    *bgProc
		want  string // expected state word
	}{
		// #23 completed-job state word.
		{"done-zero-default", false, doneBg(1, "true", 0), "Done"},
		{"done-zero-posix", true, doneBg(1, "true", 0), "Done"},
		{"done-nonzero-default", false, doneBg(1, "false", 3), "Exit 3"},
		{"done-nonzero-posix", true, doneBg(1, "false", 3), "Done(3)"},
		// #24 stopped-job state word.
		{"stopped-default", false, stoppedBg(1, "sleep 10", "SIGSTOP"), "Stopped"},
		{"stopped-posix-stop", true, stoppedBg(1, "sleep 10", "SIGSTOP"), "Stopped(SIGSTOP)"},
		{"stopped-posix-tstp", true, stoppedBg(1, "sleep 10", "SIGTSTP"), "Stopped(SIGTSTP)"},
		// POSIX stopped without a recorded signal degrades to bare "Stopped".
		{"stopped-posix-nosig", true, stoppedBg(1, "sleep 10", ""), "Stopped"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, buf := newJobFmtRunner(t, tc.posix)
			jobs := []*bgProc{tc.bg}
			r.formatJob(jobs, tc.bg, false, false)
			got := stateWord(t, buf.String())
			if got != tc.want {
				t.Fatalf("formatJob state = %q, want %q (line %q)", got, tc.want, buf.String())
			}
		})
	}
}

// #49 bg builtin output line omits the +/- marker in POSIX mode.
func TestBgJobLineMarker(t *testing.T) {
	tests := []struct {
		name  string
		posix bool
		want  string
	}{
		{"default", false, "[1]+ sleep 10 &\n"},
		{"posix", true, "[1] sleep 10 &\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newJobFmtRunner(t, tc.posix)
			bg := stoppedBg(1, "sleep 10", "SIGTSTP")
			jobs := []*bgProc{bg}
			got := r.bgJobLine(jobs, bg)
			if got != tc.want {
				t.Fatalf("bgJobLine = %q, want %q", got, tc.want)
			}
		})
	}
}
