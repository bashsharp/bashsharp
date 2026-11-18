// Copyright (c) 2026, the bashy authors
// See LICENSE for licensing information

package interp

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// runStrictReturnScript runs src on a fresh runner, optionally with
// WithStrictPosix, returning stdout and the run's exit status code.
func runStrictReturnScript(t *testing.T, strict bool, src string) (string, uint8) {
	t.Helper()
	file, err := syntax.NewParser().Parse(strings.NewReader(src), "")
	if err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	opts := []RunnerOption{StdIO(nil, &stdout, nil)}
	if strict {
		opts = append(opts, WithStrictPosix(true))
	}
	r, err := New(opts...)
	if err != nil {
		t.Fatal(err)
	}
	var code uint8
	if err := r.Run(context.Background(), file); err != nil {
		status, ok := IsExitStatus(err)
		if !ok {
			t.Fatalf("run error for %q: %v", src, err)
		}
		code = status
	}
	return stdout.String(), code
}

// TestStrictPosixReturnDefaultInTrap covers yash return-p:148 "default exit
// status in function in trap": under strictPosix, a bare `return` inside a
// FUNCTION executed during a trap action defaults to the $? from just before
// the action began, not to the last command run inside the function (which is
// what bash does, and what non-strict mode must keep reporting).
//
// The EXIT-trap variants are fully synchronous. The USR1 variants are also
// deterministic here: a self-directed `kill -s USR1 $$` goes through
// markPendingSignal, not OS signal delivery, and the handler runs at the next
// statement boundary (the trailing `:`), so no real-signal race is involved.
func TestStrictPosixReturnDefaultInTrap(t *testing.T) {
	t.Parallel()

	const exitTrapSrc = "fn() { true; return; }; trap 'fn; echo trapped $?' EXIT; (exit 19)"
	// yash return-p:148, adapted from tests/return-p.tst.
	const usr1TrapSrc = "set -o posix; fn() { true; return; }; " +
		"trap 'fn; echo trapped $?' USR1; (exit 19); (kill -s USR1 $$; exit 19); :"

	tests := []struct {
		name     string
		strict   bool
		src      string
		want     string
		wantCode uint8
	}{
		{"strict-exit-trap", true, exitTrapSrc, "trapped 19\n", 19},
		{"strict-usr1-trap", true, usr1TrapSrc, "trapped 19\n", 0},
		// Control: without strict, bash semantics stay byte-identical —
		// fn's bare return yields true's status (0), per bash 5.3 in both
		// default and posix mode.
		{"bash-exit-trap", false, exitTrapSrc, "trapped 0\n", 19},
		{"bash-usr1-trap", false, usr1TrapSrc, "trapped 0\n", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, code := runStrictReturnScript(t, tc.strict, tc.src)
			if got != tc.want {
				t.Fatalf("wrong output for %q\nwant: %q\ngot:  %q", tc.src, tc.want, got)
			}
			if code != tc.wantCode {
				t.Fatalf("wrong exit code for %q: want %d, got %d", tc.src, tc.wantCode, code)
			}
		})
	}
}

// TestStrictPosixReturnOutsideTrapUnchanged pins down that strictPosix only
// affects `return` during trap actions: the ordinary default (status of the
// last command executed in the function or sourced script) is untouched.
func TestStrictPosixReturnOutsideTrapUnchanged(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		src  string
		want string
	}{
		{
			"function-default",
			"fn() { (exit 3); return; }; fn; echo $?",
			"3\n",
		},
		{
			"function-after-trap-set",
			// A trap merely being set must not leak into non-trap returns.
			"trap 'echo unused' USR1; fn() { (exit 5); return; }; fn; echo $?",
			"5\n",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, code := runStrictReturnScript(t, true, tc.src)
			if got != tc.want {
				t.Fatalf("wrong output for %q\nwant: %q\ngot:  %q", tc.src, tc.want, got)
			}
			if code != 0 {
				t.Fatalf("wrong exit code for %q: want 0, got %d", tc.src, code)
			}
		})
	}
}
