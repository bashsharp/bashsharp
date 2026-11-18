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

// The yash builtins-p cases (builtins-p.tst:136-201) define a function named
// after each POSIX special builtin and then invoke that name: the definition
// must be accepted silently and the invocation must reach the builtin, never
// the function. Each case expects empty stdout, empty stderr and exit 0.
var strictPosixSpecialBuiltinCases = []struct {
	name string
	src  string
}{
	{"break", "break() { echo not reached; }; for i in 1; do break; done"},
	{"continue", "continue() { echo not reached; }; for i in 1; do continue; done"},
	{"eval", "eval() { echo not reached; }; eval ''"},
	{"exec", "exec() { echo not reached; }; exec"},
	{"exit", "exit() { echo not reached; }; exit"},
	{"export", "export() { echo not reached; }; export a=a"},
	{"readonly", "readonly() { echo not reached; }; readonly a=a"},
	{"return", "return() { echo not reached; }; fn() { return; }; fn"},
	{"set", "set() { echo not reached; }; set ''"},
	{"shift", "shift() { echo not reached; }; shift 0"},
	{"times", "times() { echo not reached >&2; }; times"},
	{"trap", "trap() { echo not reached; }; trap - TERM"},
	{"unset", "unset() { echo not reached; }; unset unset"},
	// colon and dot already behave this way; guard against regressions.
	{"colon", ":() { echo not reached; }; :"},
	{"dot", ". () { echo not reached; }; . /dev/null"},
}

func TestStrictPosixSpecialBuiltinFuncNames(t *testing.T) {
	t.Parallel()
	for _, tc := range strictPosixSpecialBuiltinCases {
		t.Run(tc.name, func(t *testing.T) {
			file, err := syntax.NewParser().Parse(strings.NewReader(tc.src), "")
			if err != nil {
				t.Fatal(err)
			}
			var stdout, stderr bytes.Buffer
			r, err := New(WithPosixMode(true), WithStrictPosix(true),
				StdIO(nil, &stdout, &stderr))
			if err != nil {
				t.Fatal(err)
			}
			err = r.Run(context.Background(), file)
			if status, ok := IsExitStatus(err); ok && status != 0 {
				t.Fatalf("exit status %d, want 0", status)
			} else if err != nil && !ok {
				t.Fatalf("unexpected error: %v", err)
			}
			// `times` writes its own report to stdout; only stderr matters.
			if tc.name != "times" && stdout.String() != "" {
				t.Errorf("stdout = %q, want empty", stdout.String())
			}
			if stderr.String() != "" {
				t.Errorf("stderr = %q, want empty", stderr.String())
			}
		})
	}
}

// Without the strict flag, plain POSIX mode must keep bash 5.3's behavior:
// the definition is a fatal error that kills the shell.
func TestPosixModeSpecialBuiltinFuncNameStillFatal(t *testing.T) {
	t.Parallel()
	const src = "break() { echo not reached; }; echo after"
	file, err := syntax.NewParser().Parse(strings.NewReader(src), "")
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	r, err := New(WithPosixMode(true), StdIO(nil, &stdout, &stderr))
	if err != nil {
		t.Fatal(err)
	}
	err = r.Run(context.Background(), file)
	status, ok := IsExitStatus(err)
	if !ok || status != 1 {
		t.Fatalf("Run error = %v, want exit status 1", err)
	}
	if !strings.Contains(stderr.String(), "`break': is a special builtin") {
		t.Errorf("stderr = %q, want the special builtin diagnostic", stderr.String())
	}
	if stdout.String() != "" {
		t.Errorf("stdout = %q, want empty (shell must exit)", stdout.String())
	}
}
