// Copyright (c) 2026, the bashy authors
// See LICENSE for licensing information

package interp

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

func runStrictPosixAssignError(t *testing.T, command string, strict, interactive bool) (string, string, error, bool) {
	t.Helper()
	src := "f() { printf 'function ran\\n'; }\n" +
		"readonly a=a\na=b " + command + "\nprintf 'not reached status=%s\\n' \"$?\"\n"
	file, err := syntax.NewParser().Parse(strings.NewReader(src), "")
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	r, err := New(
		WithPosixMode(true),
		WithStrictPosix(strict),
		StdIO(nil, &stdout, &stderr),
	)
	if err != nil {
		t.Fatal(err)
	}
	r.interactiveShell = interactive
	err = r.Run(context.Background(), file)
	return stdout.String(), stderr.String(), err, r.Exited()
}

func TestStrictPosixPrefixAssignErrorExits(t *testing.T) {
	commands := []string{
		"[", "alias", "array", "bg", "bindkey", "cat", "cd", "command",
		"complete", "dirs", "disown", "echo", "false", "fc", "fg",
		"getopts", "hash", "help", "history", "jobs", "kill", "popd",
		"printf", "pushd", "pwd", "read", "suspend", "test", "true",
		"type", "typeset", "ulimit", "umask", "unalias", "wait",
		"./_no_such_command_",
	}
	for _, command := range commands {
		t.Run(command, func(t *testing.T) {
			stdout, stderr, err, exited := runStrictPosixAssignError(t, command, true, false)
			if strings.Contains(stdout, "not reached") {
				t.Fatalf("following command ran; stdout = %q", stdout)
			}
			if !strings.Contains(stderr, "readonly variable") {
				t.Fatalf("stderr = %q, want readonly diagnostic", stderr)
			}
			var status ExitStatus
			if !errors.As(err, &status) || status == 0 {
				t.Fatalf("Run error = %v, want nonzero exit status", err)
			}
			if !exited {
				t.Fatal("runner did not report exiting")
			}
		})
	}
}

func TestStrictPosixPrefixAssignErrorBeforeFunctionExits(t *testing.T) {
	stdout, stderr, err, exited := runStrictPosixAssignError(t, "f", true, false)
	if strings.Contains(stdout, "not reached") {
		t.Fatalf("following command ran; stdout = %q", stdout)
	}
	if !strings.Contains(stderr, "readonly variable") {
		t.Fatalf("stderr = %q, want readonly diagnostic", stderr)
	}
	var status ExitStatus
	if !errors.As(err, &status) || status == 0 || !exited {
		t.Fatalf("Run error = %v, exited = %v; want nonzero exit and exiting runner", err, exited)
	}
}

func TestStrictPosixInteractivePrefixAssignErrorContinues(t *testing.T) {
	stdout, stderr, err, exited := runStrictPosixAssignError(t, "echo command-ran", true, true)
	if strings.Contains(stdout, "command-ran") || !strings.Contains(stdout, "not reached") {
		t.Fatalf("stdout = %q, want failed command discarded and following command run", stdout)
	}
	if !strings.Contains(stderr, "readonly variable") {
		t.Fatalf("stderr = %q, want readonly diagnostic", stderr)
	}
	if err != nil {
		t.Fatalf("Run error = %v, want success from following command", err)
	}
	if exited {
		t.Fatal("interactive runner unexpectedly exited")
	}
}

func TestNonStrictPosixReadonlyPrefixAssignErrorContinuesNextLine(t *testing.T) {
	stdout, stderr, err, exited := runStrictPosixAssignError(t, "echo command-ran", false, false)
	if strings.Contains(stdout, "command-ran") || !strings.Contains(stdout, "not reached status=1") {
		t.Fatalf("stdout = %q, want failed command discarded and following line run", stdout)
	}
	if !strings.Contains(stderr, "readonly variable") {
		t.Fatalf("stderr = %q, want readonly diagnostic", stderr)
	}
	if err != nil || exited {
		t.Fatalf("Run error = %v, exited = %v; want following line to succeed", err, exited)
	}
}

func TestNonStrictPosixReadonlyPrefixAssignErrorDiscardsSameLine(t *testing.T) {
	file, err := syntax.NewParser().Parse(strings.NewReader(
		"readonly a=a; a=b printf command-ran; printf after\n"), "")
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	r, err := New(WithPosixMode(true), StdIO(nil, &stdout, &stderr))
	if err != nil {
		t.Fatal(err)
	}
	err = r.Run(context.Background(), file)
	var status ExitStatus
	if stdout.Len() != 0 || !strings.Contains(stderr.String(), "readonly variable") ||
		!errors.As(err, &status) || status != 1 {
		t.Fatalf("stdout = %q, stderr = %q, err = %v; want same-line tail skipped with status 1",
			stdout.String(), stderr.String(), err)
	}
}

func TestNonStrictPosixInteractiveReadonlyPrefixAssignErrorContinues(t *testing.T) {
	stdout, stderr, err, exited := runStrictPosixAssignError(t, "echo command-ran", false, true)
	if strings.Contains(stdout, "command-ran") || !strings.Contains(stdout, "not reached status=1") {
		t.Fatalf("stdout = %q, want failed command discarded and following command run", stdout)
	}
	if !strings.Contains(stderr, "readonly variable") || err != nil || exited {
		t.Fatalf("stderr = %q, err = %v, exited = %v; want interactive continuation", stderr, err, exited)
	}
}
