// Copyright (c) 2026, the bashy authors
// See LICENSE for licensing information

package interp

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// These tests exercise the POSIX command grammar, selection, formatting, and
// the interpreter's synthetic job-state seams. They deliberately do not claim
// real job-control effects: the fixtures have no process group or controlling
// terminal, and therefore cannot prove SIGCONT delivery, process-group IDs,
// terminal foreground handoff, resumed terminal I/O, or waiting for a live
// foreground job. Those effects require separate PTY/process integration
// evidence and remain unverified here.
func runIssue7JobCommand(t *testing.T, src string, jobs ...*bgProc) issue7CommandResult {
	t.Helper()
	file, err := syntax.NewParser(
		syntax.Variant(syntax.LangBash),
		syntax.PosixMode(true),
	).Parse(strings.NewReader(src), "")
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	r, err := New(
		WithPosixMode(true),
		WithStrictPosix(true),
		StdIO(strings.NewReader("stdin sentinel\n"), &stdout, &stderr),
		Env(expand.ListEnviron("PATH=/bin:/usr/bin", "LANG=C", "LC_ALL=C", "POSIXLY_CORRECT=1")),
	)
	if err != nil {
		t.Fatal(err)
	}
	r.Reset()
	r.noOpSetState["monitor"] = true
	r.bgProcs = jobs
	err = r.Run(context.Background(), file)
	var status uint8
	if err != nil {
		var ok bool
		status, ok = IsExitStatus(err)
		if !ok {
			t.Fatal(err)
		}
	}
	return issue7CommandResult{stdout.String(), stderr.String(), status}
}

func TestJobsIssue7ParserAndFormatting(t *testing.T) {
	t.Run("empty_table_and_required_options", func(t *testing.T) {
		got := runIssue7JobCommand(t, "jobs\njobs -l\njobs -p")
		if got != (issue7CommandResult{}) {
			t.Fatalf("empty jobs table: %#v", got)
		}
	})
	t.Run("synthetic_pid_and_long_formatting", func(t *testing.T) {
		job := stoppedBg(1, "worker", "SIGTSTP")
		job.pid.Store(4242)
		got := runIssue7JobCommand(t, "jobs -p %1\njobs -l %1", job)
		if got.status != 0 || got.stderr != "" || !strings.HasPrefix(got.stdout, "4242\n[1]+ 4242 Stopped(SIGTSTP)") {
			t.Fatalf("jobs forms: %#v", got)
		}
	})
	t.Run("invalid_option_and_job_id_fail", func(t *testing.T) {
		for _, src := range []string{"jobs -z", "jobs %99"} {
			got := runIssue7JobCommand(t, src)
			if got.status == 0 || got.stderr == "" {
				t.Fatalf("%q: %#v", src, got)
			}
		}
	})
	t.Run("stdin_is_not_used", func(t *testing.T) {
		got := runIssue7JobCommand(t, "jobs\nIFS= read -r line\nprintf '<%s>\\n' \"$line\"")
		if got.stdout != "<stdin sentinel>\n" || got.stderr != "" || got.status != 0 {
			t.Fatalf("jobs stdin: %#v", got)
		}
	})
}

func TestBgIssue7SyntheticState(t *testing.T) {
	t.Run("marks_named_synthetic_job_running", func(t *testing.T) {
		job := stoppedBg(1, "worker", "SIGTSTP")
		job.jobControl = true
		job.pidReady = make(chan struct{})
		close(job.pidReady)
		got := runIssue7JobCommand(t, "bg %1", job)
		if got.status != 0 || got.stderr != "" || got.stdout != "[1] worker &\n" || jobStoppedState(job) {
			t.Fatalf("bg resume: result=%#v state=%v", got, job.jobState())
		}
	})
	t.Run("default_current_job_and_multiple_job_ids", func(t *testing.T) {
		one := stoppedBg(1, "one", "SIGTSTP")
		two := stoppedBg(2, "two", "SIGTSTP")
		for _, job := range []*bgProc{one, two} {
			job.jobControl = true
			job.pidReady = make(chan struct{})
			close(job.pidReady)
		}
		got := runIssue7JobCommand(t, "bg %1 %2", one, two)
		if got.status != 0 || got.stderr != "" || !strings.Contains(got.stdout, "one &") || !strings.Contains(got.stdout, "two &") {
			t.Fatalf("bg job list: %#v", got)
		}
	})
	t.Run("invalid_option_and_job_id_fail", func(t *testing.T) {
		for _, src := range []string{"bg -z", "bg %99"} {
			got := runIssue7JobCommand(t, src)
			if got.status == 0 || got.stderr == "" {
				t.Fatalf("%q: %#v", src, got)
			}
		}
	})
}

func TestFgIssue7ParserAndCompletedStatus(t *testing.T) {
	t.Run("completed_synthetic_job_propagates_status", func(t *testing.T) {
		job := doneBg(1, "worker", 7)
		job.jobControl = true
		job.pidReady = make(chan struct{})
		close(job.pidReady)
		got := runIssue7JobCommand(t, "fg %1", job)
		if got.status != 7 || got.stderr != "" || got.stdout != "worker\n" {
			t.Fatalf("fg status: %#v", got)
		}
	})
	t.Run("surplus_operand_is_rejected", func(t *testing.T) {
		got := runIssue7JobCommand(t, "fg %1 %2")
		if got.status == 0 || !strings.Contains(got.stderr, "too many arguments") {
			t.Fatalf("fg surplus operand: %#v", got)
		}
	})
	t.Run("invalid_option_and_job_id_fail", func(t *testing.T) {
		for _, src := range []string{"fg -z", "fg %99"} {
			got := runIssue7JobCommand(t, src)
			if got.status == 0 || got.stderr == "" {
				t.Fatalf("%q: %#v", src, got)
			}
		}
	})
}
