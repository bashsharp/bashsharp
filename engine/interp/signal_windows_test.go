// Copyright (c) 2026, the outpost authors
// See LICENSE for licensing information

//go:build windows

package interp_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Sprint 245, story S245.4: Windows signal delivery end to end. The table,
// bus, marker and wait-status logic are proven platform-neutrally in
// signal_wintable_test.go and signalbus_test.go; these run the real
// syscalls (named pipe, TerminateProcess) on a Windows worker only.

// winSignalHelperEnv selects the helper role a re-executed test binary plays
// (see init below): the test binary's TestMain only knows GOSH_CMD values
// declared in interp_test.go, so this file claims its own variable.
const winSignalHelperEnv = "GOSH_WIN_SIGNAL_HELPER"

// winSignalIgnoreEnv names the signals the server helper treats as
// hard-ignored on entry. The runner strips BASHY_HARD_IGNORE from every
// child's environment (it is only bridged on a self re-exec), so the helper
// carries it under its own name and installs it itself, as the CLI does.
const winSignalIgnoreEnv = "GOSH_WIN_SIGNAL_IGNORE"

func init() {
	switch os.Getenv(winSignalHelperEnv) {
	case "server":
		winSignalServerHelper()
	case "exit143":
		// A plain exit status that must never be mistaken for SIGTERM.
		os.Exit(143)
	}
}

// winSignalServerHelper is a child bashy stand-in: it serves the signal pipe,
// installs the marker default action, announces readiness on stdout and
// loops (in-process sleeps, so the trap fires at a statement boundary) until
// a USR1 trap (echo got; exit 0) or a default-action signal ends it.
func winSignalServerHelper() {
	stop, err := interp.StartProcessSignalServer()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	defer stop()
	interp.SetProcessSignalDefault(func(num int) { os.Exit(interp.SignalMarkerExitCode(num)) })
	src := "trap 'echo got; exit 0' USR1\necho ready\nwhile :; do sleep 0.05; done\necho eof\n"
	file, err := syntax.NewParser().Parse(strings.NewReader(src), "")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	opts := []interp.RunnerOption{
		interp.StdIO(os.Stdin, os.Stdout, os.Stderr),
		interp.ExecHandlers(testExecHandler),
		interp.WithSignalResetter(interp.OSSignalResetter{}),
	}
	if ign := os.Getenv(winSignalIgnoreEnv); ign != "" {
		opts = append(opts, interp.Env(expand.ListEnviron(append(os.Environ(), interp.BashyHardIgnoreEnv+"="+ign)...)))
	}
	r, err := interp.New(opts...)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if err := r.Run(context.Background(), file); err != nil {
		var status interp.ExitStatus
		if errors.As(err, &status) {
			os.Exit(int(status))
		}
		os.Exit(1)
	}
	os.Exit(0)
}

func runWinSignalScript(t *testing.T, src string, opts ...interp.RunnerOption) (string, error) {
	t.Helper()
	file := parse(t, nil, src)
	var out bytes.Buffer
	opts = append([]interp.RunnerOption{
		interp.StdIO(nil, &out, &out),
		interp.ExecHandlers(testExecHandler),
	}, opts...)
	r, err := interp.New(opts...)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	err = r.Run(ctx, file)
	return out.String(), err
}

// A trap on a full-table signal, fired by a self-directed kill from the
// shell itself, from a foreground subshell and from a background goroutine
// job — the trap.tests traffic that timed out before the bus existed.
func TestWindowsSelfSignalRunsTrap(t *testing.T) {
	out, err := runWinSignalScript(t, `
trap 'echo got' USR1
kill -USR1 $$
echo one
(kill -USR1 $$)
echo two
{ kill -USR1 $$; } &
wait
sleep 0.2
echo three
`)
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	want := "got\none\ngot\ntwo\ngot\nthree\n"
	if out != want {
		t.Fatalf("output = %q, want %q", out, want)
	}
}

// The mapfile1.sub / set-x1.sub trap line: every signal number in the table
// is a valid trap spec, and `kill -l N` / $BASH_TRAPSIG agree with it.
func TestWindowsTrapAcceptsFullTable(t *testing.T) {
	out, err := runWinSignalScript(t, `
trap 'rm -f $FILE' 0 1 2 3 6 15
trap 'echo sig=$BASH_TRAPSIG' USR2 WINCH
kill -l 1 15 30 31 64
kill -l 143
kill -USR2 $$
kill -WINCH $$
trap - USR2 WINCH
kill -WINCH $$
echo rc=$?
`)
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	want := "HUP\nTERM\nUSR1\nUSR2\nRTMAX\nTERM\nsig=31\nsig=28\nrc=0\n"
	if out != want {
		t.Fatalf("output = %q, want %q", out, want)
	}
}

// An ignored signal stays ignored on the bus, and a bridged hard ignore
// (BASHY_HARD_IGNORE) survives a trap attempt, as on Unix.
func TestWindowsIgnoredSignalsAreDropped(t *testing.T) {
	out, err := runWinSignalScript(t, `
trap '' USR2
{ kill -USR2 $$; } &
wait
trap 'echo bad' USR1; trap - USR1
kill -USR1 $$
{ kill -USR1 $$; } &
wait
sleep 0.2
echo after
`, interp.Env(expand.ListEnviron(interp.BashyHardIgnoreEnv+"=USR1")),
		interp.WithSignalResetter(interp.OSSignalResetter{}))
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	if out != "after\n" {
		t.Fatalf("output = %q, want %q", out, "after\n")
	}
}

// A child bashy (this test binary re-executed with the signal server
// running) receives `kill -USR1 <pid>` over its named pipe and runs its
// trap; a default-action signal ends it with the marker, which the parent
// reports as 128+n; a hard-ignored signal (BASHY_HARD_IGNORE) is dropped by
// the child's bus.
func TestWindowsSignalPipeToChildBashy(t *testing.T) {
	for _, tc := range []struct {
		name    string
		env     string
		kills   string
		wantOut string
		wantRC  string
	}{
		{"trap", "", "kill -USR1 $pid", "got", "rc=0"},
		{"default", "", "kill -TERM $pid", "", "rc=143"},
		{"hard_ignore", winSignalIgnoreEnv + "=USR2", "kill -USR2 $pid; sleep 0.2; kill -USR1 $pid", "got", "rc=0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			outFile := filepath.Join(dir, "child.out")
			out, err := runWinSignalScript(t, fmt.Sprintf(`
%s %s=server "$GOSH_PROG" > %q &
pid=$!
until [ -s %q ]; do sleep 0.05; done
%s
wait $pid
echo rc=$?
`, tc.env, winSignalHelperEnv, outFile, outFile, tc.kills))
			if err != nil {
				t.Fatalf("run: %v\n%s", err, out)
			}
			childOut, _ := os.ReadFile(outFile)
			got := strings.Fields(string(childOut))
			if len(got) == 0 || got[0] != "ready" || (tc.wantOut != "" && (len(got) < 2 || got[1] != tc.wantOut)) || strings.Contains(string(childOut), "eof") {
				t.Errorf("child output = %q, want ready then %q", childOut, tc.wantOut)
			}
			if !strings.Contains(out, tc.wantRC+"\n") {
				t.Errorf("parent output = %q, want %q", out, tc.wantRC)
			}
		})
	}
}

// `kill -TERM` on an external process without a signal pipe terminates it
// with the marker: `wait` reports 143, a foreground death prints
// "Terminated", and a plain `exit 143` is never mistaken for a signal.
func TestWindowsTerminateExternalReportsSignal(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pid")
	out, err := runWinSignalScript(t, fmt.Sprintf(`
GOSH_CMD=pid_and_hang "$GOSH_PROG" > %q &
pid=$!
until [ -s %q ]; do sleep 0.05; done
kill -TERM $pid
wait $pid
echo bg=$?
{ until [ -s %q.2 ]; do sleep 0.05; done; kill -TERM $(cat %q.2); } &
GOSH_CMD=pid_and_hang "$GOSH_PROG" > %q.2
echo fg=$?
wait
%s=exit143 "$GOSH_PROG"
echo plain=$?
`, pidFile, pidFile, pidFile, pidFile, pidFile, winSignalHelperEnv))
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "bg=143\n") {
		t.Errorf("background kill: output = %q, want bg=143", out)
	}
	if !strings.Contains(out, "Terminated ") {
		t.Errorf("foreground kill: output = %q, want a Terminated notice", out)
	}
	if !strings.Contains(out, "fg=143\n") {
		t.Errorf("foreground kill: output = %q, want fg=143", out)
	}
	if !strings.Contains(out, "plain=143\n") || strings.Count(out, "Terminated") != 1 {
		t.Errorf("plain exit 143: output = %q, want plain=143 without a Terminated notice", out)
	}
}

// `kill -0` probes an external process without touching it.
func TestWindowsKillZeroProbe(t *testing.T) {
	cmd := exec.Command(os.Getenv("GOSH_PROG"))
	cmd.Env = append(os.Environ(), "GOSH_CMD=pid_and_hang")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	out, err := runWinSignalScript(t, fmt.Sprintf("kill -0 %d; echo live=$?; kill -0 4000000000; echo dead=$?", cmd.Process.Pid))
	if err != nil {
		t.Fatalf("run: %v\n%s", err, out)
	}
	if !strings.Contains(out, "live=0\n") || !strings.Contains(out, "dead=1\n") {
		t.Errorf("output = %q", out)
	}
}
