// Copyright (c) 2026, the bashy authors
// See LICENSE for licensing information

package interp

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Conductor-owned boundary coverage for strict-POSIX builtin PATH gating,
// mirroring yash builtins-p.tst:243-379 ("... can be invoked without $PATH",
// setup 'PATH=; unset PATH'): special builtins and the POSIX.1-2024
// intrinsic utilities are PATH-independent, while every other builtin must
// be found by a PATH search. The positive assertion used throughout is
// deliberately weak — "did NOT die of command-not-found (127)" — because
// several exempt names legitimately fail for other reasons in a bare
// runner (bg/fg without job control, read on a closed stdin, break outside
// a loop); what the gate must never do is report them missing.

// strictPosixIntrinsics is the full intrinsic-utility list asserted by the
// yash fixture (bg/fc/fg/ulimit are commented out of yash's own runs but
// still classified intrinsic there).
var strictPosixIntrinsics = []string{
	"alias", "bg", "cd", "command", "fc", "fg", "getopts", "hash",
	"jobs", "kill", "read", "type", "ulimit", "umask", "unalias", "wait",
}

func runStrictPosixNoPath(t *testing.T, invocation string) (stdout, stderr string, code uint8) {
	t.Helper()
	dir := t.TempDir()
	src := "unset PATH\n" + invocation + "\n"
	file, err := syntax.NewParser().Parse(strings.NewReader(src), "")
	if err != nil {
		t.Fatal(err)
	}
	var outBuf, errBuf bytes.Buffer
	r, err := New(
		WithStrictPosix(true),
		StdIO(nil, &outBuf, &errBuf),
		Dir(dir),
		// No PATH in the environment at all; HOME so `cd` has a target.
		Env(expand.ListEnviron("HOME="+dir)),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), file); err != nil {
		if status, ok := IsExitStatus(err); ok {
			code = status
		} else {
			t.Fatalf("run error for %q: %v", invocation, err)
		}
	}
	return outBuf.String(), errBuf.String(), code
}

// TestStrictPosixIntrinsicClassification pins the classification tables
// themselves: the intrinsic set is exactly the yash list, and no name is
// double-classified as both special and intrinsic.
func TestStrictPosixIntrinsicClassification(t *testing.T) {
	t.Parallel()
	for _, name := range strictPosixIntrinsics {
		if !isStrictPosixIntrinsic(name) {
			t.Errorf("isStrictPosixIntrinsic(%q) = false, want true (yash builtins-p intrinsic list)", name)
		}
		if isPosixSpecialBuiltin(name) {
			t.Errorf("%q classified both intrinsic and special", name)
		}
	}
	for _, name := range []string{"builtin", "echo", "printf", "pwd", "test", "true", "false", ":", "break", "eval", "exec", "exit", "export", "readonly", "return", "set", "shift", "times", "trap", "unset"} {
		if isStrictPosixIntrinsic(name) {
			t.Errorf("isStrictPosixIntrinsic(%q) = true, want false", name)
		}
	}
}

// TestStrictPosixSpecialsRunWithoutPath: every POSIX special builtin stays
// invocable with no PATH (yash builtins-p.tst:243-306).
func TestStrictPosixSpecialsRunWithoutPath(t *testing.T) {
	t.Parallel()
	invocations := map[string]string{
		"colon":    ":",
		"dot":      ": > f.sh; . ./f.sh",
		"break":    "for i in 1; do break; done",
		"continue": "for i in 1; do continue; done",
		"eval":     "eval :",
		"exec":     "exec",
		"exit":     "exit 0",
		"export":   "export STRICT_F=1",
		"readonly": "readonly STRICT_R=1",
		"return":   "fn() { return 0; }; fn",
		"set":      "set --",
		"shift":    "shift 0",
		"times":    "times",
		"trap":     "trap",
		"unset":    "unset STRICT_F",
	}
	for name, inv := range invocations {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, stderr, code := runStrictPosixNoPath(t, inv)
			if code == 127 || strings.Contains(stderr, "command not found") {
				t.Fatalf("special builtin %s gated behind PATH: code=%d stderr=%q", name, code, stderr)
			}
		})
	}
}

// TestStrictPosixIntrinsicsRunWithoutPath: every intrinsic utility stays
// invocable with no PATH (yash builtins-p.tst:315-379).
func TestStrictPosixIntrinsicsRunWithoutPath(t *testing.T) {
	t.Parallel()
	invocations := map[string]string{
		"alias":   "alias",
		"bg":      "bg",
		"cd":      "cd",
		"command": "command :",
		"fc":      "fc -l",
		"fg":      "fg",
		"getopts": "getopts a opt",
		"hash":    "hash",
		"jobs":    "jobs",
		"kill":    "kill -l",
		"read":    "read strict_v",
		"type":    "type cd",
		"ulimit":  "ulimit",
		"umask":   "umask",
		"unalias": "unalias -a",
		"wait":    "wait",
	}
	for _, name := range strictPosixIntrinsics {
		inv, ok := invocations[name]
		if !ok {
			t.Fatalf("no invocation for intrinsic %q — table drifted", name)
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, stderr, code := runStrictPosixNoPath(t, inv)
			if code == 127 || strings.Contains(stderr, "command not found") {
				t.Fatalf("intrinsic %s gated behind PATH: code=%d stderr=%q", name, code, stderr)
			}
		})
	}
}

// TestStrictPosixBuiltinDispatcherRunsWithoutPath covers Bash's explicit
// builtin dispatcher. It is a Bash extension rather than a POSIX intrinsic,
// but Bash keeps it in-process and PATH-independent in POSIX mode. This also
// matters for executable facades for shell builtins, whose body is equivalent
// to `builtin cd ...`.
func TestStrictPosixBuiltinDispatcherRunsWithoutPath(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	target := filepath.Join(home, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}

	file, err := syntax.NewParser().Parse(strings.NewReader(
		`unset PATH; builtin cd "$HOME/target"; builtin printf '%s\n' "$PWD"`), "")
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	r, err := New(
		WithStrictPosix(true),
		StdIO(nil, &stdout, &stderr),
		Dir(home),
		Env(expand.ListEnviron("HOME="+home)),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), file); err != nil {
		t.Fatalf("builtin dispatcher with no PATH: %v; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	if r.Dir != target || stderr.String() != "" || stdout.String() != target+"\n" {
		t.Fatalf("builtin dispatcher with no PATH: dir=%q stdout=%q stderr=%q; want %q, %q, empty",
			r.Dir, stdout.String(), stderr.String(), target, target+"\n")
	}
}

// TestStrictPosixRegularBuiltinsGated: a broader negative table than the
// worker's echo/pwd pair — regular builtins die 127 with no PATH. `false`
// is the sharpest case: 127 proves the gate fired before the builtin ran
// (it would exit 1 otherwise).
func TestStrictPosixRegularBuiltinsGated(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"echo", "printf", "pwd", "test", "true", "false"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			stdout, stderr, code := runStrictPosixNoPath(t, name+" x")
			if code != 127 {
				t.Fatalf("%s with no PATH: code=%d, want 127; stdout=%q", name, code, stdout)
			}
			if !strings.Contains(stderr, "command not found") {
				t.Fatalf("%s: stderr=%q, want command-not-found diagnostic", name, stderr)
			}
			if stdout != "" {
				t.Fatalf("%s produced stdout %q despite failed lookup", name, stdout)
			}
		})
	}
}
