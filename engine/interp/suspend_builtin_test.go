// Copyright (c) 2026, the outpost authors
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

// Sprint 245, story S245.8: jobs.tests:231-233 ends on three `suspend`
// forms. bash's suspend_builtin refuses through sh_nojobs whenever
// job_control is 0 — which a non-interactive shell always is, on any
// platform — so the message is the same here as on a Unix bash, and the
// Windows job-control work does not change it. Locked down so it cannot
// drift while the rest of the job table moves.

// runShellScript runs src through a fresh runner and returns its stdout,
// stderr and exit status.
func runShellScript(t *testing.T, src string) (stdout, stderr string, status uint8) {
	t.Helper()
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(src), "suspend.tests")
	if err != nil {
		t.Fatal(err)
	}
	var out, errBuf bytes.Buffer
	r, err := New(
		StdIO(nil, &out, &errBuf),
		WithBashCompatErrors(true),
		Env(expand.ListEnviron("PATH=/bin:/usr/bin", "LANG=C", "LC_ALL=C")),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), file); err != nil {
		code, ok := IsExitStatus(err)
		if !ok {
			t.Fatal(err)
		}
		status = code
	}
	return out.String(), errBuf.String(), status
}

func TestSuspendBuiltinRefusesWithoutJobControl(t *testing.T) {
	stdout, stderr, status := runShellScript(t, "set +m\nsuspend -z\nsuspend\nsuspend --\n")
	if stdout != "" {
		t.Errorf("suspend wrote %q to stdout", stdout)
	}
	if status == 0 {
		t.Error("suspend succeeded without job control")
	}
	want := []string{
		"suspend.tests: line 2: suspend: -z: invalid option",
		"suspend: usage: suspend [-f]",
		"suspend.tests: line 3: suspend: cannot suspend: no job control",
		"suspend.tests: line 4: suspend: cannot suspend: no job control",
	}
	got := strings.Split(strings.TrimRight(stderr, "\n"), "\n")
	if len(got) != len(want) {
		t.Fatalf("suspend wrote %d stderr lines, want %d:\n%s", len(got), len(want), stderr)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("stderr line %d is %q, want %q", i+1, got[i], want[i])
		}
	}
}
