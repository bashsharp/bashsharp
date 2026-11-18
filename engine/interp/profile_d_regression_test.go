// Copyright (c) 2026, the bashy authors
// See LICENSE for licensing information

//go:build unix

package interp_test

// Focused regressions for the VSC-PCTS Profile D findings reconciled against
// run profile-d-s82-c287d82 (sh 8a7b94fe). Each test mirrors the failing
// certification assertion narrowly; the harness- and cushim-owned failures
// from the same run are documented in docs/profile-d-s82-disposition.md.

import (
	"bytes"
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func runProfileDScript(t *testing.T, src string, opts ...interp.RunnerOption) (stdout, stderr string, status uint8) {
	t.Helper()
	file := parse(t, syntax.NewParser(), src)
	var outBuf, errBuf bytes.Buffer
	opts = append([]interp.RunnerOption{
		interp.StdIO(nil, &outBuf, &errBuf),
	}, opts...)
	r, err := interp.New(opts...)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), runnerRunTimeout)
	defer cancel()
	err = r.Run(ctx, file)
	if code, ok := interp.IsExitStatus(err); ok {
		status = code
	} else if err != nil {
		t.Fatal(err)
	}
	return outBuf.String(), errBuf.String(), status
}

// POSIX.cmd/command assertion 51 (GA39): `command -v` writing to a broken
// pipe while SIGPIPE is ignored must report a diagnostic on standard error
// and return non-zero, like bash's sh_chkwrite in command.def.
func TestCommandVReportsBrokenPipeWriteError(t *testing.T) {
	t.Parallel()
	src := `exec 3>&1
(
	trap "" PIPE
	sleep 0.1
	command -v true
	echo exitstatus $? >&3
) | true
`
	stdout, stderr, status := runProfileDScript(t, src)
	if status != 0 {
		t.Fatalf("script status = %d, want 0 (stderr: %q)", status, stderr)
	}
	if !strings.Contains(stdout, "exitstatus 1") {
		t.Fatalf("stdout = %q, want failing exit status from command -v", stdout)
	}
	if !strings.Contains(stderr, "command: write error") {
		t.Fatalf("stderr = %q, want a command write-error diagnostic", stderr)
	}
}

// POSIX.cmd/kill assertion 8: bash 5.3 maps `kill -l 0` to the pseudo-signal
// EXIT (a normally-exited status names no signal), while 128 stays invalid.
func TestKillListZeroPrintsExit(t *testing.T) {
	t.Parallel()
	stdout, _, status := runProfileDScript(t, "kill -l 0")
	if status != 0 || stdout != "EXIT\n" {
		t.Fatalf("kill -l 0 = %q status %d, want \"EXIT\\n\" status 0", stdout, status)
	}
	_, stderr, status := runProfileDScript(t, "kill -l 128")
	if status == 0 || !strings.Contains(stderr, "invalid signal specification") {
		t.Fatalf("kill -l 128 status %d stderr %q, want invalid-signal error", status, stderr)
	}
}

// POSIX.upe/time assertions 814/816/819: the `time -p` report must use the
// radix character of the current locale (LC_ALL > LC_NUMERIC > LANG). The
// runner carries the Profile D de_DE ISO-8859-1 definition; it must not infer
// the same radix for other German locales such as de_CH.
func TestTimePosixLocaleRadix(t *testing.T) {
	t.Parallel()
	commaRe := regexp.MustCompile(`(?m)^real \d+,\d\d$`)
	dotRe := regexp.MustCompile(`(?m)^real \d+\.\d\d$`)

	_, stderr, status := runProfileDScript(t, "time -p true",
		interp.Env(expand.ListEnviron("LC_ALL=de_DE.iso88591")))
	if status != 0 || !commaRe.MatchString(stderr) {
		t.Fatalf("de_DE time -p stderr = %q status %d, want comma radix", stderr, status)
	}

	_, stderr, status = runProfileDScript(t, "time -p true",
		interp.Env(expand.ListEnviron("LC_ALL=POSIX", "LANG=de_DE.iso88591")))
	if status != 0 || !dotRe.MatchString(stderr) {
		t.Fatalf("POSIX time -p stderr = %q status %d, want dot radix (LC_ALL overrides LANG)", stderr, status)
	}

	_, stderr, status = runProfileDScript(t, "time -p true",
		interp.Env(expand.ListEnviron("LC_NUMERIC=de_DE.iso88591")))
	if status != 0 || !commaRe.MatchString(stderr) {
		t.Fatalf("LC_NUMERIC time -p stderr = %q status %d, want comma radix", stderr, status)
	}

	for _, locale := range []string{
		"de_CH.ISO-8859-1",
		"de_DE.UTF-8",
		"fr_FR.ISO-8859-1",
	} {
		_, stderr, status = runProfileDScript(t, "time -p true",
			interp.Env(expand.ListEnviron("LC_ALL="+locale)))
		if status != 0 || !dotRe.MatchString(stderr) {
			t.Fatalf("unsupported locale %s time -p stderr = %q status %d, want dot fallback",
				locale, stderr, status)
		}
	}
}

// POSIX.shell/shell sh_09 assertion 520 (first phase): SIGINT and SIGQUIT
// delivered by the kill builtin to an asynchronous list started without job
// control must be discarded — a fork-based shell's async children inherit
// SIG_IGN for both (POSIX 2.11) — while other signals still terminate it.
func TestAsyncKillIntQuitInheritedIgnore(t *testing.T) {
	t.Parallel()

	src := `(sleep 0.2; echo alive) &
p=$!
kill -s INT $p || echo kill_int_failed
kill -s QUIT $p || echo kill_quit_failed
wait $p
echo st=$?
`
	stdout, stderr, status := runProfileDScript(t, src)
	if status != 0 || stdout != "alive\nst=0\n" {
		t.Fatalf("async INT/QUIT: stdout %q stderr %q status %d, want job to survive with status 0",
			stdout, stderr, status)
	}

	// SIGTERM keeps its default disposition and must still kill the job.
	src = `(sleep 5; echo alive) &
kill -s TERM $!
wait $!
echo st=$?
`
	stdout, _, status = runProfileDScript(t, src)
	if status != 0 || stdout != "st=143\n" {
		t.Fatalf("async TERM: stdout %q status %d, want st=143 and no output", stdout, status)
	}

	// The same inherited-ignore rule covers an exec'd external child that
	// $! resolves to directly.
	src = `sleep 0.2 &
p=$!
kill -s INT $p
kill -s QUIT $p
wait $p
echo st=$?
`
	stdout, _, status = runProfileDScript(t, src, interp.Env(nil))
	if status != 0 || stdout != "st=0\n" {
		t.Fatalf("async external INT/QUIT: stdout %q status %d, want st=0", stdout, status)
	}
}
