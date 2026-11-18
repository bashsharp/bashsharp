// Copyright (c) 2025, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package interp_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// TestHashBuiltin_P records a path with `hash -p` and verifies that
// `hash -t` reports it.
func TestHashBuiltin_P(t *testing.T) {
	t.Parallel()

	// Create a temporary executable.
	dir := t.TempDir()
	exe := filepath.Join(dir, "mycmd")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\necho hello\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	var stdout strings.Builder
	runner, err := interp.New(
		interp.StdIO(nil, &stdout, nil),
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// `hash -p /tmp/mycmd mycmd; hash -t mycmd`
	prog := `hash -p ` + exe + ` mycmd; hash -t mycmd`
	file, err := syntax.NewParser().Parse(strings.NewReader(prog), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(ctx, file); err != nil {
		t.Fatalf("hash -p; hash -t failed: %v", err)
	}
	got := strings.TrimSpace(stdout.String())
	if got != exe {
		t.Fatalf("hash -t printed %q, want %q", got, exe)
	}
}

// TestHashBuiltin_P_missingArgs verifies that `hash -p` without a
// path or without names produces a usage error.
func TestHashBuiltin_P_missingArgs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		script string
		want   string // substring of stderr
	}{
		{
			script: "hash -p",
			want:   "hash: -p: option requires an argument",
		},
		{
			script: "hash -p /bin/echo",
			want:   "hash: -p: option requires an argument",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.script, func(t *testing.T) {
			t.Parallel()
			var stderr strings.Builder
			runner, err := interp.New(
				interp.StdIO(nil, nil, &stderr),
			)
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			file, err := syntax.NewParser().Parse(strings.NewReader(tc.script), "")
			if err != nil {
				t.Fatal(err)
			}
			_ = runner.Run(ctx, file) // ignore error; we check stderr
			if !strings.Contains(stderr.String(), tc.want) {
				t.Fatalf("stderr = %q, want substring %q", stderr.String(), tc.want)
			}
		})
	}
}

func TestHashBuiltinTargetOptionsNeedNames(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, script, wantErr, wantOut string
	}{
		{"target", "hash -t; printf 'status=%s\\n' \"$?\"", "hash: -t: option requires an argument\n", "status=1\n"},
		{"clear_then_target", "hash -r -t missing; printf 'status=%s\\n' \"$?\"", "hash: missing: not found\n", "status=1\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr strings.Builder
			runner, err := interp.New(interp.StdIO(nil, &stdout, &stderr))
			if err != nil {
				t.Fatal(err)
			}
			file, err := syntax.NewParser().Parse(strings.NewReader(tc.script), "")
			if err != nil {
				t.Fatal(err)
			}
			if err := runner.Run(context.Background(), file); err != nil {
				t.Fatal(err)
			}
			if got := stderr.String(); !strings.Contains(got, tc.wantErr) {
				t.Fatalf("stderr = %q, want substring %q", got, tc.wantErr)
			}
			if got := stdout.String(); got != tc.wantOut {
				t.Fatalf("stdout = %q, want %q", got, tc.wantOut)
			}
		})
	}
}

func TestHashBuiltinAutomaticallyCachesCommands(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	exe := filepath.Join(dir, "cached-command")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr strings.Builder
	runner, err := interp.New(interp.StdIO(nil, &stdout, &stderr))
	if err != nil {
		t.Fatal(err)
	}
	script := `PATH=` + dir + `; cached-command; hash -t cached-command`
	file, err := syntax.NewParser().Parse(strings.NewReader(script), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background(), file); err != nil {
		t.Fatalf("run failed: %v; stderr: %s", err, stderr.String())
	}
	if got, want := stdout.String(), exe+"\n"; got != want {
		t.Fatalf("stdout = %q, want %q; stderr: %s", got, want, stderr.String())
	}
}

func TestHashBuiltinFirstLookupPreservesHandlerCommandName(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	exe := filepath.Join(dir, "handled-command")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	var stdout strings.Builder
	var handledName string
	runner, err := interp.New(
		interp.StdIO(nil, &stdout, nil),
		interp.ExecHandlers(func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
			return func(ctx context.Context, args []string) error {
				handledName = args[0]
				return nil
			}
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	script := `PATH=` + dir + `; handled-command; hash -t handled-command`
	file, err := syntax.NewParser().Parse(strings.NewReader(script), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background(), file); err != nil {
		t.Fatal(err)
	}
	if handledName != "handled-command" {
		t.Fatalf("handler command = %q, want original name", handledName)
	}
	if got, want := stdout.String(), exe+"\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestHashBuiltinDisabledDoesNotCacheCommands(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	exe := filepath.Join(dir, "uncached-command")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr strings.Builder
	runner, err := interp.New(interp.StdIO(nil, &stdout, &stderr))
	if err != nil {
		t.Fatal(err)
	}
	script := `PATH=` + dir + `; set +h; uncached-command; set -h; hash -t uncached-command; printf 'status=%s\n' "$?"`
	file, err := syntax.NewParser().Parse(strings.NewReader(script), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background(), file); err != nil {
		t.Fatal(err)
	}
	if got, want := stdout.String(), "status=1\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if got, want := stderr.String(), "hash: uncached-command: not found\n"; got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
}

func TestHashBuiltinPathAssignmentInvalidatesCache(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	firstDir := filepath.Join(dir, "first")
	secondDir := filepath.Join(dir, "second")
	if err := os.Mkdir(firstDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(secondDir, 0o755); err != nil {
		t.Fatal(err)
	}
	firstExe := filepath.Join(firstDir, "moving-command")
	secondExe := filepath.Join(secondDir, "moving-command")
	if err := os.WriteFile(firstExe, []byte("#!/bin/sh\necho first\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondExe, []byte("#!/bin/sh\necho second\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr strings.Builder
	runner, err := interp.New(interp.StdIO(nil, &stdout, &stderr))
	if err != nil {
		t.Fatal(err)
	}
	script := `PATH=` + firstDir + `; moving-command; PATH=` + secondDir + `; moving-command; hash -t moving-command`
	file, err := syntax.NewParser().Parse(strings.NewReader(script), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background(), file); err != nil {
		t.Fatalf("run failed: %v; stderr: %s", err, stderr.String())
	}
	if got, want := stdout.String(), "first\nsecond\n"+secondExe+"\n"; got != want {
		t.Fatalf("stdout = %q, want %q; stderr: %s", got, want, stderr.String())
	}
}

func TestHashBuiltinDoesNotCacheNonExternalCommands(t *testing.T) {
	t.Parallel()

	var stdout, stderr strings.Builder
	runner, err := interp.New(interp.StdIO(nil, &stdout, &stderr))
	if err != nil {
		t.Fatal(err)
	}
	script := `hash -r; sample_function() { :; }; sample_function; printf x >/dev/null; missing_command >/dev/null 2>&1; hash -t sample_function printf missing_command >/dev/null; printf 'status=%s\n' "$?"`
	file, err := syntax.NewParser().Parse(strings.NewReader(script), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background(), file); err != nil {
		t.Fatal(err)
	}
	if got, want := stdout.String(), "status=1\n"; got != want {
		t.Fatalf("stdout = %q, want %q; stderr: %s", got, want, stderr.String())
	}
	wantErr := "hash: sample_function: not found\n" +
		"hash: printf: not found\n" +
		"hash: missing_command: not found\n"
	if got := stderr.String(); got != wantErr {
		t.Fatalf("stderr = %q, want %q", got, wantErr)
	}
}

func TestHashEmptyListingPosix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		script string
		want   string
	}{
		{
			name:   "default",
			script: "hash",
			want:   "hash: hash table empty\n",
		},
		{
			name:   "posix",
			script: "set -o posix; hash",
			want:   "",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			file, err := syntax.NewParser().Parse(strings.NewReader(tc.script), "")
			if err != nil {
				t.Fatal(err)
			}
			var stdout strings.Builder
			runner, err := interp.New(
				interp.StdIO(nil, &stdout, nil),
			)
			if err != nil {
				t.Fatal(err)
			}
			if err := runner.Run(context.Background(), file); err != nil {
				t.Fatal(err)
			}
			if got := stdout.String(); got != tc.want {
				t.Fatalf("stdout = %q, want %q", got, tc.want)
			}
		})
	}
}
