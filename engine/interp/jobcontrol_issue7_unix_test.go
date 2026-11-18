// Copyright (c) 2026, the bashy authors
// See LICENSE for licensing information

//go:build unix

package interp_test

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

var jobIdentityPattern = regexp.MustCompile(`(?:STOP|TTY)_ID pid=([0-9]+) pgrp=([0-9]+)`)

func startJobControlPTY(t *testing.T, helper string) (*os.File, *exec.Cmd, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	cmd := exec.CommandContext(ctx, os.Getenv("GOSH_PROG"))
	cmd.Env = append(os.Environ(), "GOSH_CMD="+helper)
	primary, err := pty.Start(cmd)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	return primary, cmd, cancel
}

func normalizePTYOutput(out string) string { return strings.ReplaceAll(out, "\r", "") }

func assertRealJobIdentity(t *testing.T, out string) string {
	t.Helper()
	match := jobIdentityPattern.FindStringSubmatch(out)
	if match == nil {
		t.Fatalf("missing child pid/pgrp identity:\n%s", out)
	}
	if match[1] != match[2] {
		t.Fatalf("child pid %s is not its process-group leader %s:\n%s", match[1], match[2], out)
	}
	if !strings.Contains(out, "JOBS_P="+match[2]+"\n") {
		t.Fatalf("jobs -p did not report process-group ID %s:\n%s", match[2], out)
	}
	if !strings.Contains(out, "JOBS_L=[1]+ "+match[2]+" Stopped") {
		t.Fatalf("jobs -l did not report stopped process group %s:\n%s", match[2], out)
	}
	return match[2]
}

func TestBgIssue7SendsContinueToRealProcessGroup(t *testing.T) {
	primary, cmd, cancel := startJobControlPTY(t, "job_control_bg_shell")
	defer cancel()
	defer primary.Close()
	data, readErr := io.ReadAll(primary)
	waitErr := cmd.Wait()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		// PTY masters commonly return EIO after the slave closes; the complete
		// process output and wait status below are authoritative.
		if !strings.Contains(strings.ToLower(readErr.Error()), "input/output error") {
			t.Fatal(readErr)
		}
	}
	if waitErr != nil {
		t.Fatalf("job-control helper: %v; output=%q", waitErr, data)
	}
	out := normalizePTYOutput(string(data))
	assertRealJobIdentity(t, out)
	if !strings.Contains(out, "STOP_CONTINUED\n") || !strings.Contains(out, "BG_STATUS=5\n") {
		t.Fatalf("bg did not continue and reap the stopped group:\n%s", out)
	}
}

func TestFgIssue7OwnsTerminalReadsWaitsAndRestores(t *testing.T) {
	primary, cmd, cancel := startJobControlPTY(t, "job_control_fg_shell")
	defer cancel()
	defer primary.Close()

	lines := make(chan string, 32)
	go func() {
		scanner := bufio.NewScanner(primary)
		for scanner.Scan() {
			lines <- scanner.Text() + "\n"
		}
		close(lines)
	}()
	var output strings.Builder
	waitFor := func(want string) {
		t.Helper()
		deadline := time.NewTimer(5 * time.Second)
		defer deadline.Stop()
		for !strings.Contains(normalizePTYOutput(output.String()), want) {
			select {
			case line, ok := <-lines:
				if !ok {
					t.Fatalf("PTY closed before %q; output=%q", want, output.String())
				}
				output.WriteString(line)
			case <-deadline.C:
				t.Fatalf("timed out waiting for %q; output=%q", want, output.String())
			}
		}
	}

	waitFor("TTY_CONTINUED\n")
	if _, err := primary.Write([]byte("terminal payload\n")); err != nil {
		t.Fatal(err)
	}
	waitFor("SHELL_RESTORED")
	if err := cmd.Wait(); err != nil {
		t.Fatalf("job-control helper: %v; output=%q", err, output.String())
	}
	for line := range lines {
		output.WriteString(line)
	}
	out := normalizePTYOutput(output.String())
	assertRealJobIdentity(t, out)
	if !strings.Contains(out, "TTY_GOT=terminal payload\n") || !strings.Contains(out, "FG_STATUS=7\n") {
		t.Fatalf("fg did not provide terminal input and propagate status:\n%s", out)
	}
	restored := regexp.MustCompile(`SHELL_RESTORED pgrp=([0-9]+) foreground=([0-9]+)`).FindStringSubmatch(out)
	if restored == nil || restored[1] != restored[2] {
		t.Fatalf("shell foreground process group was not restored:\n%s", out)
	}
	if _, err := strconv.Atoi(restored[1]); err != nil {
		t.Fatalf("invalid restored process group %q: %v", restored[1], err)
	}
}

func TestFgUsesControllingTerminalWhenStdinIsRedirected(t *testing.T) {
	primary, cmd, cancel := startJobControlPTY(t, "job_control_redirected_parent")
	defer cancel()
	defer primary.Close()
	data, readErr := io.ReadAll(primary)
	waitErr := cmd.Wait()
	if readErr != nil && !errors.Is(readErr, io.EOF) &&
		!strings.Contains(strings.ToLower(readErr.Error()), "input/output error") {
		t.Fatal(readErr)
	}
	if waitErr != nil {
		t.Fatalf("redirected job-control helper: %v; output=%q", waitErr, data)
	}
	out := normalizePTYOutput(string(data))
	if !strings.Contains(out, "REDIRECTED_FG_STATUS=0\n") {
		t.Fatalf("fg did not use /dev/tty with redirected stdin:\n%s", out)
	}
}
