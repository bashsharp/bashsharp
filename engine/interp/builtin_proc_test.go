// Copyright (c) 2026, the outpost authors
// See LICENSE for licensing information

//go:build unix

package interp_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	qt "github.com/go-quicktest/qt"
	"golang.org/x/sys/unix"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// safeBuf is a goroutine-safe wrapper around bytes.Buffer. Backgrounded
// statements that spawn real exec.Cmd children get a copy goroutine
// inside os/exec that writes to this buffer concurrently with the
// parent shell's writes — so a plain bytes.Buffer would race.
type safeBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}
func (b *safeBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// runScript parses and runs src under a fresh Runner, returning stdout+stderr
// merged and any execution error.
func runScript(t *testing.T, src string) (string, error) {
	t.Helper()
	file, err := syntax.NewParser().Parse(strings.NewReader(src), "")
	qt.Assert(t, qt.IsNil(err))
	buf := &safeBuf{}
	r, err := interp.New(interp.StdIO(nil, buf, buf))
	qt.Assert(t, qt.IsNil(err))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	runErr := r.Run(ctx, file)
	return buf.String(), runErr
}

func TestAsyncListExternalCommandIgnoresIntQuitWithoutJobControl(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh:", err)
	}
	out, err := runScript(t, `set +m
/bin/sh -c 'trap - INT QUIT; kill -INT $$; kill -QUIT $$; printf "survived\n"' &
wait $!
echo "rc=$?"`)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("out: %q", out))
	qt.Assert(t, qt.Equals(out, "survived\nrc=0\n"))
}

func TestKillSendsRealSignal(t *testing.T) {
	sleepBin, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep binary on PATH:", err)
	}
	// Spawn a 30s sleep externally so the runner gets a known real PID to
	// signal. The runner's own "&" backgrounding can't be used because $!
	// returns a "g<N>" sentinel, not a real PID.
	cmd := exec.Command(sleepBin, "30")
	qt.Assert(t, qt.IsNil(cmd.Start()))
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	script := fmt.Sprintf("kill %d", cmd.Process.Pid)
	out, err := runScript(t, script)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("runScript output: %q", out))

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case waitErr := <-done:
		qt.Assert(t, qt.IsNotNil(waitErr), qt.Commentf("sleep should have been signalled"))
		var ee *exec.ExitError
		qt.Assert(t, qt.ErrorAs(waitErr, &ee))
		ws, ok := ee.Sys().(syscall.WaitStatus)
		qt.Assert(t, qt.IsTrue(ok))
		qt.Assert(t, qt.IsTrue(ws.Signaled()))
		qt.Assert(t, qt.Equals(ws.Signal(), syscall.SIGTERM))
	case <-time.After(5 * time.Second):
		t.Fatal("sleep did not exit within 5s of receiving SIGTERM")
	}
}

func TestKillExistenceProbe(t *testing.T) {
	sleepBin, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep binary on PATH:", err)
	}
	cmd := exec.Command(sleepBin, "30")
	qt.Assert(t, qt.IsNil(cmd.Start()))
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	// signal 0 = existence probe, must succeed for a live PID
	out, err := runScript(t, fmt.Sprintf("kill -0 %d && echo alive", cmd.Process.Pid))
	qt.Assert(t, qt.IsNil(err), qt.Commentf("out: %q", out))
	qt.Assert(t, qt.Equals(strings.TrimSpace(out), "alive"))

	// 99999999 should not exist on any reasonable system. The kernel
	// returns ESRCH which our builtin reports as a non-zero exit.
	if runtime.GOOS == "darwin" || runtime.GOOS == "linux" {
		out, err = runScript(t, "kill -0 99999999 || echo dead")
		qt.Assert(t, qt.IsNil(err), qt.Commentf("out: %q", out))
		qt.Assert(t, qt.IsTrue(strings.Contains(out, "dead")))
	}
}

func TestKillSignalSpecThenNegativePidTarget(t *testing.T) {
	// Regression: once a signal has been established via `-s`/`-n`, a later
	// dash-prefixed argument must not be reinterpreted as another signal
	// spec — it may be a negative pid naming a process group instead. Real
	// bash 5.3's kill_builtin (builtins/kill.def) only guards the *bare*
	// -SIGSPEC form with `saw_signal == 0`; `-s`/`-n` themselves don't gate
	// that check, so `kill -s TERM -99999999` targets process group
	// 99999999 rather than erroring. Before the fix, this builtin's flag
	// loop kept treating every dash-prefixed argument as a candidate flag
	// and reported "99999999: invalid signal specification" instead.
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("process-group signalling behavior is unix-specific")
	}
	out, err := runScript(t, "kill -s TERM -99999999 || echo dead")
	qt.Assert(t, qt.IsNil(err), qt.Commentf("out: %q", out))
	qt.Assert(t, qt.IsFalse(strings.Contains(out, "invalid signal specification")), qt.Commentf("out: %q", out))
	qt.Assert(t, qt.IsTrue(strings.Contains(out, "dead")), qt.Commentf("out: %q", out))
}

func TestKillIssue7SignalSpellingsAndExitStatus(t *testing.T) {
	out, err := runScript(t, `
printf 'status=%s\n' "$(kill -l 131)"
printf 'mixed=%s\n' "$(kill -l urG)"
printf 'upper=%s\n' "$(kill -l URG)"
`)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("out: %q", out))
	lines := strings.Fields(out)
	qt.Assert(t, qt.HasLen(lines, 3), qt.Commentf("out: %q", out))
	qt.Assert(t, qt.Equals(lines[0], "status=QUIT"), qt.Commentf("out: %q", out))
	qt.Assert(t, qt.Equals(lines[1], strings.Replace(lines[2], "upper=", "mixed=", 1)), qt.Commentf("out: %q", out))
}

func TestKillZeroTargetsCallingProcessGroup(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("process-group signalling behavior is unix-specific")
	}
	dir := t.TempDir()
	parentMarker := filepath.Join(dir, "parent")
	childMarker := filepath.Join(dir, "child")
	readyMarker := filepath.Join(dir, "ready")
	script := `
trap 'printf x > "$KILL_PARENT_MARKER"; exit 0' TERM
"$GOSH_PROG" 'trap '\''printf x > "$KILL_CHILD_MARKER"; exit 0'\'' TERM
printf x > "$KILL_READY_MARKER"
while :; do /bin/sleep 30; done' &
while [ ! -f "$KILL_READY_MARKER" ]; do /bin/sleep 0.05; done
kill 0
/bin/sleep 3
exit 99
`
	cmd := exec.Command(os.Args[0], script)
	cmd.Env = append(os.Environ(),
		"GOSH_PROG="+os.Args[0],
		"KILL_PARENT_MARKER="+parentMarker,
		"KILL_CHILD_MARKER="+childMarker,
		"KILL_READY_MARKER="+readyMarker,
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("kill 0 process-group helper: %v; output=%q", err, out)
	}
	for _, marker := range []string{parentMarker, childMarker} {
		if _, err := os.Stat(marker); err != nil {
			t.Fatalf("process-group member did not catch SIGTERM (%s): %v", filepath.Base(marker), err)
		}
	}
}

func TestKillZeroRunsShellTrapBeforeNextStatement(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("process-group signalling behavior is unix-specific")
	}
	for _, tc := range []struct {
		name string
		kill string
	}{
		{name: "zero", kill: "kill 0"},
		{name: "negative-current-group", kill: `kill -s TERM "-$$"`},
		{name: "foreground-subshell", kill: "(kill 0)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			orderMarker := filepath.Join(dir, "order")
			readyMarker := filepath.Join(dir, "ready")
			script := `
trap 'printf T >> "$KILL_ORDER_MARKER"' TERM
"$GOSH_PROG" 'trap '\''exit 0'\'' TERM
printf x > "$KILL_READY_MARKER"
while :; do /bin/sleep 0.05; done' &
worker=$!
while [ ! -f "$KILL_READY_MARKER" ]; do /bin/sleep 0.05; done
` + tc.kill + `
printf K >> "$KILL_ORDER_MARKER"
wait "$worker"
printf 'W%s' "$?" >> "$KILL_ORDER_MARKER"
`
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], script)
			cmd.Env = append(os.Environ(),
				"GOSH_PROG="+os.Args[0],
				"KILL_ORDER_MARKER="+orderMarker,
				"KILL_READY_MARKER="+readyMarker,
			)
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("group-trap helper: %v; output=%q", err, out)
			}
			got, err := os.ReadFile(orderMarker)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != "TKW0" {
				t.Fatalf("trap/post-kill/wait order = %q, want TKW0", got)
			}
		})
	}
}

func TestKillCustomSignal(t *testing.T) {
	sleepBin, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("no sleep binary on PATH:", err)
	}
	cmd := exec.Command(sleepBin, "30")
	qt.Assert(t, qt.IsNil(cmd.Start()))
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()

	out, err := runScript(t, fmt.Sprintf("kill -KILL %d", cmd.Process.Pid))
	qt.Assert(t, qt.IsNil(err), qt.Commentf("out: %q", out))

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case waitErr := <-done:
		var ee *exec.ExitError
		qt.Assert(t, qt.ErrorAs(waitErr, &ee))
		ws := ee.Sys().(syscall.WaitStatus)
		qt.Assert(t, qt.IsTrue(ws.Signaled()))
		qt.Assert(t, qt.Equals(ws.Signal(), syscall.SIGKILL))
	case <-time.After(5 * time.Second):
		t.Fatal("sleep did not exit within 5s of receiving SIGKILL")
	}
}

// psSessionKey returns the `ps -o` keyword that prints the session-leader
// PID on this OS. Linux/Solaris use "sid"; the BSDs (macOS, FreeBSD) use
// "sess".
func psSessionKey() string {
	switch runtime.GOOS {
	case "darwin", "freebsd", "netbsd", "openbsd", "dragonfly":
		return "sess"
	default:
		return "sid"
	}
}

func requireSessionPS(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ps"); err != nil {
		t.Skip("no ps on PATH:", err)
	}
	cmd := exec.Command("sh", "-c", fmt.Sprintf("ps -o %s= -p $$", psSessionKey()))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("ps cannot inspect sessions: %v; out: %q", err, out)
	}
}

func TestSetsidNewSession(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh on PATH:", err)
	}
	requireSessionPS(t)
	script := fmt.Sprintf(`setsid sh -c 'ps -o %s= -p $$'`, psSessionKey())
	out, err := runScript(t, script)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("out: %q", out))
	childSID, parseErr := strconv.Atoi(strings.TrimSpace(out))
	qt.Assert(t, qt.IsNil(parseErr), qt.Commentf("expected numeric SID, got %q", out))

	// Get our own session id via getsid(0).
	mySID, err := unix.Getsid(0)
	qt.Assert(t, qt.IsNil(err))

	qt.Assert(t, qt.Not(qt.Equals(childSID, mySID)),
		qt.Commentf("child SID %d should differ from runner SID %d", childSID, mySID))
}

func TestNohupNoTTYInheritsStdio(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh on PATH:", err)
	}
	// Stdout is a bytes.Buffer (not a tty), so per POSIX nohup should
	// inherit it — and the child's `echo hello` should land directly in
	// our buffer, NOT in a nohup.out file.
	tmpDir := t.TempDir()
	prevDir, _ := os.Getwd()
	qt.Assert(t, qt.IsNil(os.Chdir(tmpDir)))
	defer os.Chdir(prevDir)

	out, err := runScript(t, "nohup sh -c 'echo hello'")
	qt.Assert(t, qt.IsNil(err), qt.Commentf("out: %q", out))
	qt.Assert(t, qt.Equals(strings.TrimSpace(out), "hello"))

	_, statErr := os.Stat(filepath.Join(tmpDir, "nohup.out"))
	qt.Assert(t, qt.IsTrue(os.IsNotExist(statErr)),
		qt.Commentf("nohup.out should not be created when stdout is not a tty"))
}

// TestDisabledBuiltinNohupFallsThroughToExternal covers BOTH paths of the
// fork's nohup builtin: kept as a builtin (the AgentOS / matrix-shell default,
// for in-process detach across a closed SSH session) and disabled via
// [interp.WithDisabledBuiltins] (the programmatic `enable -n`, how the pure
// `bash` drop-in stays strict bash 5.3 — which has no nohup builtin). It also
// asserts the disabled state survives Reset (bashy calls Reset before each run;
// preserving disabledBuiltins there was the fix that made the drop-in work).
func TestDisabledBuiltinNohupFallsThroughToExternal(t *testing.T) {
	runOut := func(t *testing.T, src string, reset bool, opts ...interp.RunnerOption) string {
		t.Helper()
		file, err := syntax.NewParser().Parse(strings.NewReader(src), "")
		qt.Assert(t, qt.IsNil(err))
		buf := &safeBuf{}
		base := []interp.RunnerOption{interp.StdIO(nil, buf, buf)}
		r, err := interp.New(append(base, opts...)...)
		qt.Assert(t, qt.IsNil(err))
		if reset {
			r.Reset() // mirror bashy's run() path; disabled set must survive
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = r.Run(ctx, file)
		return strings.TrimSpace(buf.String())
	}

	// Internal: default fork behavior — nohup is a builtin.
	qt.Assert(t, qt.Equals(runOut(t, "type -t nohup", false), "builtin"))

	// External: WithDisabledBuiltins makes it fall through to PATH. type -t is
	// "file" when /usr/bin/nohup exists, "" when absent — never "builtin".
	dis := interp.WithDisabledBuiltins("nohup")
	got := runOut(t, "type -t nohup", false, dis)
	qt.Assert(t, qt.Not(qt.Equals(got, "builtin")),
		qt.Commentf("disabled nohup must not report builtin; got %q", got))

	// Critical regression: the disabled state survives Reset (preserving
	// disabledBuiltins in Reset is the fix that made the bash drop-in work).
	qt.Assert(t, qt.Equals(runOut(t, "type -t nohup", true, dis),
		runOut(t, "type -t nohup", false, dis)),
		qt.Commentf("WithDisabledBuiltins must survive Reset (bashy resets before each run)"))

	// Functional: with a real external nohup present, the disabled path must
	// still RUN the command (do its job, not just reclassify). Non-tty stdout
	// (a buffer) means nohup inherits it per POSIX, so output lands here.
	if _, err := exec.LookPath("nohup"); err != nil {
		t.Skip("no external nohup on PATH:", err)
	}
	qt.Assert(t, qt.Equals(runOut(t, "type -t nohup", false, dis), "file"))
	out := runOut(t, "nohup sh -c 'echo hello'", false, dis)
	qt.Assert(t, qt.IsTrue(strings.Contains(out, "hello")),
		qt.Commentf("external nohup must run the command; got %q", out))
}

// TestCompgenActionFlags covers the compgen action flags added for bash
// fidelity: the -W literal word-list mode and the -v/-e shorthands, plus the
// two exit-code contracts (no candidates → 1, invalid action → 2). The
// FS/PATH/etc-backed shorthands (-f/-d/-c/-u/-g/-s) are validated against real
// bash 5.3 in the umbrella's container diff rather than re-derived here.
func TestCompgenActionFlags(t *testing.T) {
	contains := func(src, want string) {
		t.Helper()
		out, _ := runScript(t, src)
		qt.Assert(t, qt.IsTrue(strings.Contains(out, want)),
			qt.Commentf("src %q out %q want %q", src, out, want))
	}
	notContains := func(src, bad string) {
		t.Helper()
		out, _ := runScript(t, src)
		qt.Assert(t, qt.IsFalse(strings.Contains(out, bad)),
			qt.Commentf("src %q out %q should not contain %q", src, out, bad))
	}
	exact := func(src, want string) {
		t.Helper()
		out, _ := runScript(t, src)
		qt.Assert(t, qt.Equals(strings.TrimSpace(out), want), qt.Commentf("src %q", src))
	}

	// -W: literal word list, prefix-filtered (a distinct mode, not an -A type).
	contains(`compgen -W "alpha beta gamma alef" a`, "alpha")
	contains(`compgen -W "alpha beta gamma alef" a`, "alef")
	notContains(`compgen -W "alpha beta gamma" a`, "beta")
	// -v: shell variables; -e: exported only.
	contains(`zvarx=1; compgen -v zvar`, "zvarx")
	exact(`export ZEXPORTED=1; compgen -e ZEXPORTED`, "ZEXPORTED")
	exact(`ZLOCAL=1; compgen -e ZLOCAL; echo "rc=$?"`, "rc=1") // set but not exported
	// Exit-code contracts.
	exact(`compgen -W "x y" zzz; echo "rc=$?"`, "rc=1")           // no candidates → 1
	exact(`compgen -A bogus x 2>/dev/null; echo "rc=$?"`, "rc=2") // invalid action → 2
}

// TestDollarBangReturnsRealPid is the canonical regression test for the
// PID=$!; kill $PID bash idiom. Before the fix, $! returned a "g1"
// sentinel that the kill builtin (correctly) rejected as a non-PID.
func TestDollarBangReturnsRealPid(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("no sleep on PATH:", err)
	}
	// sleep 5 & PID=$!; echo "pid=$PID"
	// We then introspect: the printed PID must be numeric, and `kill -0
	// $PID` must report "alive" because sleep is genuinely running.
	out, err := runScript(t, `sleep 5 & PID=$!; echo "pid=$PID"; kill -0 $PID && echo alive; kill $PID; wait $PID 2>/dev/null; echo done`)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("out: %q", out))
	lines := strings.Split(strings.TrimSpace(out), "\n")
	qt.Assert(t, qt.IsTrue(len(lines) >= 3), qt.Commentf("expected >=3 lines, got %q", out))
	pidLine := lines[0]
	qt.Assert(t, qt.IsTrue(strings.HasPrefix(pidLine, "pid=")),
		qt.Commentf("first line should be pid= prefixed: %q", pidLine))
	pidStr := strings.TrimPrefix(pidLine, "pid=")
	pid, perr := strconv.Atoi(pidStr)
	qt.Assert(t, qt.IsNil(perr), qt.Commentf("$! must be numeric, got %q", pidStr))
	qt.Assert(t, qt.IsTrue(pid > 0), qt.Commentf("PID must be > 0, got %d", pid))
	qt.Assert(t, qt.IsTrue(strings.Contains(out, "alive")),
		qt.Commentf("kill -0 should report alive; out=%q", out))
}

func TestWaitPidConsumesReapedStatus(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("no sleep on PATH:", err)
	}
	out, err := runScript(t, `sleep 0.1 & p=$!; wait $p; echo "rc1=$?"; wait $p 2>/dev/null; echo "rc2=$?"`)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("out: %q", out))
	qt.Assert(t, qt.Equals(strings.TrimSpace(out), "rc1=0\nrc2=127"))
}

// TestDollarBangFallsBackToSentinel: when the backgrounded statement is
// pure-builtin (no real exec), there's no OS PID to report. $! must
// fall back to the legacy "g<N>" sentinel so `wait $!` (which the
// pure-builtin case can still meaningfully wait for) still works.
func TestDollarBangFallsBackToSentinel(t *testing.T) {
	out, err := runScript(t, `true & echo "bg=$!"; wait $!; echo done`)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("out: %q", out))
	first := strings.SplitN(strings.TrimSpace(out), "\n", 2)[0]
	qt.Assert(t, qt.Equals(first, "bg=g1"),
		qt.Commentf("pure-builtin bg should yield g1 sentinel: %q", out))
}

func TestNohupChildIsInNewSession(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh on PATH:", err)
	}
	requireSessionPS(t)
	script := fmt.Sprintf(`nohup sh -c 'ps -o %s= -p $$'`, psSessionKey())
	out, err := runScript(t, script)
	qt.Assert(t, qt.IsNil(err), qt.Commentf("out: %q", out))
	childSID, parseErr := strconv.Atoi(strings.TrimSpace(out))
	qt.Assert(t, qt.IsNil(parseErr), qt.Commentf("expected numeric SID, got %q", out))
	mySID, err := unix.Getsid(0)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Not(qt.Equals(childSID, mySID)),
		qt.Commentf("nohup child SID %d should differ from runner SID %d", childSID, mySID))
}

// TestWithBgPidCallback covers the WithBgPidCallback RunnerOption: when
// a backgrounded statement spawns a real external process, the embedder's
// callback must fire exactly once with the kernel PID. Outpost wires this
// to its persistent job-control registry (`outpost jobs/fg/bg/kill`).
func TestWithBgPidCallback(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("no sleep on PATH:", err)
	}
	gotPid := make(chan int, 1)
	file, err := syntax.NewParser().Parse(strings.NewReader(`sleep 30 & wait $!`), "")
	qt.Assert(t, qt.IsNil(err))
	r, err := interp.New(
		interp.StdIO(nil, io.Discard, io.Discard),
		interp.WithBgPidCallback(func(pid int) {
			select {
			case gotPid <- pid:
			default:
			}
		}),
	)
	qt.Assert(t, qt.IsNil(err))

	// Run the script in a goroutine so we can kill the sleep child once
	// we've observed the callback, instead of waiting 30s for it to exit.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	runErrCh := make(chan error, 1)
	go func() { runErrCh <- r.Run(ctx, file) }()

	select {
	case pid := <-gotPid:
		qt.Assert(t, qt.IsTrue(pid > 0), qt.Commentf("callback PID must be > 0, got %d", pid))
		// Kill the sleep so `wait $!` returns promptly.
		_ = syscall.Kill(pid, syscall.SIGTERM)
	case <-time.After(3 * time.Second):
		cancel()
		<-runErrCh
		t.Fatal("WithBgPidCallback was not invoked within 3s")
	}
	<-runErrCh
}

// TestWithBgPidCallback_NotCalledForPureBuiltin: backgrounded statements
// that never `exec.Start` (pure-builtin / pure-goroutine subshells) have
// no kernel PID to report — the callback must stay silent for them, same
// rationale as the `g<N>` sentinel that `$!` returns in this case.
func TestWithBgPidCallback_NotCalledForPureBuiltin(t *testing.T) {
	called := make(chan int, 1)
	file, err := syntax.NewParser().Parse(strings.NewReader(`true & wait $!`), "")
	qt.Assert(t, qt.IsNil(err))
	r, err := interp.New(
		interp.StdIO(nil, io.Discard, io.Discard),
		interp.WithBgPidCallback(func(pid int) {
			called <- pid
		}),
	)
	qt.Assert(t, qt.IsNil(err))
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	qt.Assert(t, qt.IsNil(r.Run(ctx, file)))
	select {
	case pid := <-called:
		t.Fatalf("pure-builtin bg should not fire callback, got pid=%d", pid)
	default:
	}
}

// guard against the package not being linked if errors lib changes
var _ = errors.New
