// Copyright (c) 2026 qiangli
// See LICENSE for licensing information

//go:build unix

package cli

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestProfileBShUtilityEntrypointContract drives the installed shape of the
// Profile B shell: cmd/bash reached through an executable named "sh". This is
// deliberately a process-level test. Package-only runner tests cannot prove
// argv[0] selection or the utility's -c operand, environment, stream, and exit
// status contract together.
func TestProfileBShUtilityEntrypointContract(t *testing.T) {
	bin := builtBashBin(t)
	dir := t.TempDir()
	sh := filepath.Join(dir, "sh")
	if err := os.Symlink(bin, sh); err != nil {
		t.Fatal(err)
	}

	const script = `printf 'zero=%s one=%s env=%s\n' "$0" "$1" "$ENTRY_ENV"
IFS= read -r line
printf 'stdin=%s\n' "$line"
printf 'diag=%s\n' "$2" >&2
exit 23`
	cmd := exec.Command(sh, "-c", script, "command-name", "operand-one", "operand-two")
	cmd.Env = []string{"PATH=/bin:/usr/bin", "HOME=" + dir, "ENTRY_ENV=from-environment"}
	cmd.Stdin = bytes.NewBufferString("from-standard-input\n")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 23 {
		t.Fatalf("sh exit error = %v, want status 23", err)
	}
	if got, want := stdout.String(), "zero=command-name one=operand-one env=from-environment\nstdin=from-standard-input\n"; got != want {
		t.Fatalf("sh stdout = %q, want %q", got, want)
	}
	if got, want := stderr.String(), "diag=operand-two\n"; got != want {
		t.Fatalf("sh stderr = %q, want %q", got, want)
	}
}

// Once -c has consumed its command operand, later option-looking words are
// positional parameters. In particular, POSIX sh -c '...' -s must expose -s
// as $0 and leave standard input available to the command.
func TestShCommandOperandEndsOptionParsing(t *testing.T) {
	bin := builtBashBin(t)
	dir := t.TempDir()
	sh := filepath.Join(dir, "sh")
	if err := os.Symlink(bin, sh); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(sh, "-c", `IFS= read -r line; printf 'zero=%s one=%s stdin=%s\n' "$0" "$1" "$line"`, "-s", "operand")
	cmd.Env = []string{"PATH=/bin:/usr/bin", "HOME=" + dir}
	cmd.Stdin = bytes.NewBufferString("data\n")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sh exited with %v: %s", err, out)
	}
	if got, want := string(out), "zero=-s one=operand stdin=data\n"; got != want {
		t.Fatalf("sh output = %q, want %q", got, want)
	}
}

func TestShCommandOptionBoundaryForms(t *testing.T) {
	bin := builtBashBin(t)
	dir := t.TempDir()
	sh := filepath.Join(dir, "sh")
	if err := os.Symlink(bin, sh); err != nil {
		t.Fatal(err)
	}
	const script = `case $- in *e*) e=on;; *) e=off;; esac; printf 'zero=%s one=%s errexit=%s\n' "$0" "$1" "$e"`
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"clustered-c", []string{"-ec", script, "-s", "x"}, "zero=-s one=x errexit=on\n"},
		{"option-after-c", []string{"-c", "-e", script, "x"}, "zero=x one= errexit=on\n"},
		{"double-dash-after-command", []string{"-c", script, "--", "a"}, "zero=-- one=a errexit=off\n"},
		{"s-after-command", []string{"-c", script, "-s"}, "zero=-s one= errexit=off\n"},
		{"s-before-c", []string{"-s", "-c", script, "a"}, "zero=a one= errexit=off\n"},
		{"plus-o-before-c", []string{"+o", "errexit", "-c", script, "-x"}, "zero=-x one= errexit=off\n"},
		{"empty-command", []string{"-c", "", "a"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(sh, tc.args...)
			cmd.Env = []string{"PATH=/bin:/usr/bin", "HOME=" + dir}
			out, err := cmd.CombinedOutput()
			if err != nil || string(out) != tc.want {
				t.Fatalf("sh %q: exit %v, output %q; want exit 0, output %q", tc.args, err, out, tc.want)
			}
		})
	}
	file := filepath.Join(dir, "script.sh")
	if err := os.WriteFile(file, []byte(`printf 'one=%s two=%s\n' "$1" "$2"`), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(sh, file, "-c", "operand")
	cmd.Env = []string{"PATH=/bin:/usr/bin", "HOME=" + dir}
	out, err := cmd.CombinedOutput()
	if err != nil || string(out) != "one=-c two=operand\n" {
		t.Fatalf("script operands: exit %v, output %q", err, out)
	}
}
