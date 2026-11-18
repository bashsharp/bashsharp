// Copyright (c) 2026, the bashy authors
// See LICENSE for licensing information

//go:build unix

package interp_test

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/interp"
)

// lineObserver provides a bounded, synchronized readiness oracle for tests
// where the runner writes output concurrently with the test reading it. A
// plain bytes.Buffer is not safe for that hand-off and races under -race.
type lineObserver struct {
	mu       sync.Mutex
	buf      bytes.Buffer // bashpp-racegate:safe-synchronized
	offset   int
	lines    chan string
	overflow error
}

var errLineObserverOverflow = errors.New("line observer capacity exceeded")

func newLineObserver(size int) *lineObserver {
	return &lineObserver{lines: make(chan string, size)}
}

func (o *lineObserver) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	n, err := o.buf.Write(p)
	for {
		b := o.buf.Bytes()[o.offset:]
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			return n, err
		}
		line := string(append([]byte(nil), b[:i+1]...))
		o.offset += i + 1
		select {
		case o.lines <- line:
		default:
			o.overflow = errLineObserverOverflow
			return n, o.overflow
		}
	}
}

func (o *lineObserver) Err() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.overflow
}

func (o *lineObserver) ReadLine(t *testing.T, d time.Duration) string {
	t.Helper()
	select {
	case line := <-o.lines:
		return line
	case <-time.After(d):
		t.Fatalf("timed out waiting for observed line; buffered=%q", o.String())
		return ""
	}
}

func (o *lineObserver) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buf.String()
}

func TestLineObserverReportsOverflow(t *testing.T) {
	o := newLineObserver(1)
	input := []byte("first\nsecond\n")
	n, err := o.Write(input)
	if n != len(input) || !errors.Is(err, errLineObserverOverflow) {
		t.Fatalf("Write = (%d, %v), want (%d, %v)", n, err, len(input), errLineObserverOverflow)
	}
	if !errors.Is(o.Err(), errLineObserverOverflow) {
		t.Fatalf("Err = %v, want %v", o.Err(), errLineObserverOverflow)
	}
}

type testProcessGroupCarrierProc struct{ *testCarrierProc }

func (p *testProcessGroupCarrierProc) ProcessGroupID() int { return p.Pid() }
func (p *testProcessGroupCarrierProc) ResumeProcessGroupLeader() error {
	return syscall.Kill(p.Pid(), syscall.SIGCONT)
}

func newTestProcessGroupCarrier() *testCarrier {
	return &testCarrier{
		configure: func(cmd *exec.Cmd) {
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		},
		wrap: func(proc *testCarrierProc) interp.CarrierProcess {
			return &testProcessGroupCarrierProc{testCarrierProc: proc}
		},
	}
}

var carrierPipelineIdentity = regexp.MustCompile(`(?:LEFT|RIGHT) pid=([0-9]+) pgrp=([0-9]+)`)

func TestJobCarrierOwnsStablePipelineProcessGroup(t *testing.T) {
	c := newTestProcessGroupCarrier()
	out := runCarrierScript(t, c, `
set -m
/bin/sh -c 'p=$$; g=$(/bin/ps -o pgid= -p "$p" | tr -d " "); echo "LEFT pid=$p pgrp=$g" >&2; sleep 1' |
  /bin/sh -c 'p=$$; g=$(/bin/ps -o pgid= -p "$p" | tr -d " "); echo "RIGHT pid=$p pgrp=$g" >&2; sleep 1' &
j=$(jobs -p)
wait
echo "JOBS_P=$j"
`)
	started := c.startedPids()
	if len(started) != 1 {
		t.Fatalf("carrier pids = %v, want one; output:\n%s", started, out)
	}
	wantGroup := strconv.Itoa(started[0])
	matches := carrierPipelineIdentity.FindAllStringSubmatch(out, -1)
	if len(matches) != 2 {
		t.Fatalf("pipeline identities missing:\n%s", out)
	}
	for _, match := range matches {
		if match[2] != wantGroup {
			t.Fatalf("child pid %s joined pgrp %s, want carrier group %s:\n%s", match[1], match[2], wantGroup, out)
		}
	}
	if !strings.Contains(out, "JOBS_P="+wantGroup+"\n") {
		t.Fatalf("jobs -p did not publish carrier group %s:\n%s", wantGroup, out)
	}
	waitPidsGone(t, started)
}

func TestJobCarrierSingleCommandJobsPIDMatchesDollarBang(t *testing.T) {
	c := newTestProcessGroupCarrier()
	out := runCarrierScript(t, c, `
set -m
sleep 30 &
p=$!
j=$(jobs -p %+)
echo "bang=$p jobs=$j"
kill "$p"
wait "$p" 2>/dev/null
`)
	fields := strings.Fields(strings.TrimSpace(out))
	if len(fields) != 2 || strings.TrimPrefix(fields[0], "bang=") != strings.TrimPrefix(fields[1], "jobs=") {
		t.Fatalf("$! and jobs -p disagree:\n%s", out)
	}
	waitPidsGone(t, c.startedPids())
}

// killBinPrelude locates the real kill binary, bypassing the builtin, so
// scripts can exercise the "an external process signals the job" path.
const killBinPrelude = `K=/bin/kill; [ -x "$K" ] || K=/usr/bin/kill` + "\n"

// waitPidsGone polls until none of the given PIDs exist anymore,
// failing the test if any survives the deadline. Guards against leaked
// carrier processes.
func waitPidsGone(t *testing.T, pids []int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for _, pid := range pids {
		for pidLive(pid) {
			if time.Now().After(deadline) {
				t.Fatalf("carrier pid %d still alive; leaked?", pid)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
}

func TestJobCarrierReceivesIgnoredSignalSnapshot(t *testing.T) {
	if os.Getenv("GOSH_CARRIER_SIGNAL_CHILD") == "" {
		testBinary, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(testBinary, "-test.run=^TestJobCarrierReceivesIgnoredSignalSnapshot$")
		cmd.Env = append(os.Environ(), "GOSH_PROG=", "GOSH_CARRIER_SIGNAL_CHILD=1")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("isolated carrier signal check: %v\n%s", err, output)
		}
		return
	}
	c := &ignoredSignalCarrier{
		testCarrier: new(testCarrier),
		snapshots:   make(chan []string, 1),
	}
	out := runCarrierScript(t, c, `
trap '' TERM
trap ':' HUP
{ :; } &
wait
trap - TERM HUP
`, interp.WithSignalResetter(interp.OSSignalResetter{}))
	if out != "" {
		t.Fatalf("unexpected output: %q", out)
	}
	got := <-c.snapshots
	contains := func(name string) bool {
		for _, candidate := range got {
			if candidate == name {
				return true
			}
		}
		return false
	}
	if !contains("TERM") || !contains("INT") || !contains("QUIT") {
		t.Fatalf("ignored snapshot = %v, want TERM plus async INT/QUIT", got)
	}
	if contains("HUP") {
		t.Fatalf("ignored snapshot = %v, caught HUP must not be included", got)
	}
}

// TestJobCarrierExternalKill0 checks that an external `kill -0 $!` sees
// a live kernel PID for a job with no external process of its own, and
// that the PID is gone once the job has been waited for.
func TestJobCarrierExternalKill0(t *testing.T) {
	t.Parallel()
	c := new(testCarrier)
	out := runCarrierScript(t, c, killBinPrelude+`
{ sleep 10; } &
p=$!
"$K" -0 "$p" && echo alive
"$K" -TERM "$p"
wait "$p"
echo "term=$?"
`)
	if got := strings.TrimSpace(out); got != "alive\nterm=143" {
		t.Fatalf("unexpected output:\n%s", out)
	}
	// The job carried an external `sleep 10`; Run finishing within the
	// 5s test timeout proves carrier death canceled that child too.
	waitPidsGone(t, c.startedPids())
}

// TestAsyncPipelineResetIntQuitExecInheritance pins a pipeline-specific
// signal inheritance race. The asynchronous list defaults INT/QUIT to
// ignored, but the last pipeline component explicitly resets each signal
// before replacing itself with an external command. The represented job must
// honor that reset even while the left component is starting concurrently
// with the asynchronous-list ignore.
func TestAsyncPipelineResetIntQuitExecInheritance(t *testing.T) {
	c := new(testCarrier)
	out := runCarrierScript(t, c, killBinPrelude+`
for sig in INT QUIT; do
	/bin/sleep 1 | (trap - "$sig"; : >"ready.$sig"; exec /bin/cat >/dev/null) &
	p=$!
	while [ ! -e "ready.$sig" ]; do :; done
	/bin/sleep 0.05
	"$K" -"$sig" "$p"
	wait "$p"
	echo "$?"
done
`, interp.Dir(t.TempDir()))
	if got := strings.Fields(out); len(got) != 2 || got[0] != "130" || got[1] != "131" {
		t.Fatalf("reset pipeline statuses = %q, want [130 131]", got)
	}
	waitPidsGone(t, c.startedPids())
}

// TestAsyncPipelineResetChildHandledSignal keeps the replacement child's
// actual status authoritative when it installs its own handler after the
// shell reset. The reset only determines the disposition inherited at exec;
// it must not blindly force 128+signal over the child's handled exit.
func TestAsyncPipelineResetChildHandledSignal(t *testing.T) {
	c := new(testCarrier)
	out := runCarrierScript(t, c, killBinPrelude+`
/bin/sleep 1 | (trap - INT; exec /bin/sh -c 'trap "exit 7" INT; : >childready; while :; do :; done') &
p=$!
while [ ! -e childready ]; do :; done
"$K" -INT "$p"
wait "$p"
echo "$?"
`, interp.Dir(t.TempDir()))
	if got := strings.TrimSpace(out); got != "7" {
		t.Fatalf("handled reset-pipeline status = %q, want 7", got)
	}
	waitPidsGone(t, c.startedPids())
}

// TestAsyncCompoundImplicitIntQuitRemainIgnored guards the ordinary bash
// rule on the other side of the pipeline reset case: without an explicit
// reset in the asynchronous runner, INT and QUIT remain ignored.
func TestAsyncCompoundImplicitIntQuitRemainIgnored(t *testing.T) {
	c := new(testCarrier)
	out := runCarrierScript(t, c, killBinPrelude+`
for sig in INT QUIT; do
	{ : >"ready.$sig"; /bin/sleep 0.1; } &
	p=$!
	while [ ! -e "ready.$sig" ]; do :; done
	"$K" -"$sig" "$p"
	wait "$p"
	echo "$?"
done
`, interp.Dir(t.TempDir()))
	if got := strings.Fields(out); len(got) != 2 || got[0] != "0" || got[1] != "0" {
		t.Fatalf("implicit compound statuses = %q, want [0 0]", got)
	}
	waitPidsGone(t, c.startedPids())
}

// TestNestedAsyncReinstallsImplicitIntIgnore checks that a fresh inner
// asynchronous list replaces an inherited explicit-reset marker with its own
// POSIX implicit ignore. The inner external command therefore survives INT.
func TestNestedAsyncReinstallsImplicitIntIgnore(t *testing.T) {
	c := new(testCarrier)
	out := runCarrierScript(t, c, killBinPrelude+`
(
	trap - INT
	/bin/sh -c ': >childready; exec /bin/sleep 1' &
	p=$!
	while [ ! -e childready ]; do :; done
	"$K" -INT "$p"
	wait "$p"
	echo "$?"
) &
wait
`, interp.Dir(t.TempDir()))
	if got := strings.TrimSpace(out); got != "0" {
		t.Fatalf("nested async INT status = %q, want 0", got)
	}
	waitPidsGone(t, c.startedPids())
}

// TestCompoundNestedPipelineDoesNotReplaceCarrierSignalOwner checks that a
// nested pipeline cannot leave its last component's traps as the disposition
// owner for the surrounding compound job.
func TestCompoundNestedPipelineDoesNotReplaceCarrierSignalOwner(t *testing.T) {
	defer func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGTERM)
		signal.Stop(ch)
	}()
	c := new(testCarrier)
	out := runCarrierScript(t, c, killBinPrelude+`
{
	/bin/sleep 0.05 | (trap '' TERM; /bin/cat >/dev/null)
	: >ready
	while [ ! -e go ]; do :; done
} &
p=$!
while [ ! -e ready ]; do :; done
"$K" -TERM "$p"
/bin/sleep 0.05
: >go
wait "$p"
echo "$?"
`, interp.Dir(t.TempDir()))
	if got := strings.TrimSpace(out); got != "143" {
		t.Fatalf("compound nested-pipeline TERM status = %q, want 143", got)
	}
	waitPidsGone(t, c.startedPids())
}

// TestJobCarrierTermReachesCompoundExternalChild pins the distinction between
// the requested job signal and generic runner cancellation. A compound async
// job publishes its carrier as $!, so TERM first reaches the carrier. The
// watcher must forward TERM to the external child; sending INT instead leaves
// an INT-ignoring child alive until the two-second KILL fallback, allowing it
// to mutate state after the parent considers the job terminated.
func TestJobCarrierTermReachesCompoundExternalChild(t *testing.T) {
	t.Parallel()
	c := new(testCarrier)
	dir := t.TempDir()
	out := runCarrierScript(t, c, `
{
	/bin/sh -c 'trap "" INT; trap ": > term; exit 0" TERM; : > ready; while :; do :; done'
} &
p=$!
while [ ! -e ready ]; do :; done
kill -TERM "$p"
wait "$p" 2>/dev/null || :
if [ -e term ]; then echo forwarded; else echo lost; fi
`, interp.Dir(dir))
	if got := strings.TrimSpace(out); got != "forwarded" {
		t.Fatalf("carrier TERM did not reach compound job child:\n%s", out)
	}
	waitPidsGone(t, c.startedPids())
}

// TestJobCarrierExecChildTermStatus checks that an exec-replaced asynchronous
// shell reports the external child's final status. The carrier receives TERM
// first, but if the exec'd child catches it, its chosen status must win over
// the carrier's provisional 128+TERM. An unhandled TERM remains 143.
func TestJobCarrierExecChildTermStatus(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, trap, want string
	}{
		{"HandledZero", `trap "exit 0" TERM; `, "0"},
		{"HandledSeven", `trap "exit 7" TERM; `, "7"},
		{"Default", "", "143"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := new(testCarrier)
			dir := t.TempDir()
			src := `
{
	exec /bin/sh -c 'trap "" INT; ` + tc.trap + `: > ready; while :; do :; done'
} &
p=$!
while [ ! -e ready ]; do :; done
kill -s 0 "$p"
kill "$p"
wait "$p"
echo "st=$?"
			`
			out := runCarrierScript(t, c, src, interp.Dir(dir))
			if got, want := strings.TrimSpace(out), "st="+tc.want; got != want {
				t.Fatalf("exec child signal status = %q, want %q", got, want)
			}
			waitPidsGone(t, c.startedPids())
		})
	}
}

// TestJobCarrierLateSignalDoesNotOverrideExecResult pins the other side of
// the carrier race: once an exec-replacement child has reached a terminal
// status, a signal that reaches only the still-live proxy is too late to
// rewrite that status. A real exec'd process is already a zombie or gone at
// this point; GNU bash likewise retains the child's status for `wait`.
func TestJobCarrierLateSignalDoesNotOverrideExecResult(t *testing.T) {
	t.Parallel()
	c := new(testCarrier)
	lateSignal := func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
		return func(ctx context.Context, args []string) error {
			err := next(ctx, args) // the replacement child has exited 7
			pids := c.startedPids()
			if len(pids) != 1 {
				t.Fatalf("started carrier PIDs = %v, want one", pids)
			}
			if err := syscall.Kill(pids[0], syscall.SIGTERM); err != nil {
				t.Fatalf("late carrier TERM: %v", err)
			}
			select {
			case <-ctx.Done(): // watcher stored killedSignal before canceling
			case <-time.After(runnerRunTimeout):
				t.Fatal("late carrier TERM was not observed")
			}
			return err
		}
	}
	out := runCarrierScript(t, c, `
{ exec /bin/sh -c 'exit 7'; } &
p=$!
wait "$p"
echo "st=$?"
`, interp.ExecHandlers(lateSignal))
	if got := strings.TrimSpace(out); got != "st=7" {
		t.Fatalf("late carrier signal rewrote exec result: %q", got)
	}
	waitPidsGone(t, c.startedPids())
}

// TestJobCarrierExecMiddlewareStatusWins checks that the status returned by
// the outermost ExecHandler remains authoritative. DefaultExecHandler can
// observe the raw child status, but middleware is allowed to translate or
// handle that result before returning it to the Runner.
func TestJobCarrierExecMiddlewareStatusWins(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"Rewrite", interp.NewExitStatus(9), "9"},
		{"Swallow", nil, "0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := new(testCarrier)
			translateStatus := func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
				return func(ctx context.Context, args []string) error {
					_ = next(ctx, args)
					return tc.err
				}
			}
			out := runCarrierScript(t, c, `
{ exec /bin/sh -c 'exit 7'; } &
p=$!
wait "$p"
echo "st=$?"
`, interp.ExecHandlers(translateStatus))
			if got, want := strings.TrimSpace(out), "st="+tc.want; got != want {
				t.Fatalf("exec middleware status = %q, want %q", got, want)
			}
			waitPidsGone(t, c.startedPids())
		})
	}
}

// TestJobCarrierExternalChildPublishesPID checks that a simple external
// command displaces the carrier seed in $!. wait/kill still resolve the PID
// through the job registry so signal status and child cleanup are preserved.
func TestJobCarrierExternalChildPublishesPID(t *testing.T) {
	t.Parallel()
	c := new(testCarrier)
	dir := t.TempDir()
	out := runCarrierScript(t, c, `
sh -c 'echo $$ > child.pid; exec sleep 30' &
p=$!
while [ ! -s child.pid ]; do :; done
echo "p=$p"
kill "$p"
wait "$p"
echo "st=$?"
`, interp.Dir(dir))
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "p=") || lines[1] != "st=143" {
		t.Fatalf("unexpected output:\n%s", out)
	}
	pid, err := strconv.Atoi(strings.TrimPrefix(lines[0], "p="))
	if err != nil {
		t.Fatalf("invalid child PID in %q: %v", lines[0], err)
	}
	childBytes, err := os.ReadFile(filepath.Join(dir, "child.pid"))
	if err != nil {
		t.Fatal(err)
	}
	childPID, err := strconv.Atoi(strings.TrimSpace(string(childBytes)))
	if err != nil {
		t.Fatalf("invalid child pid %q: %v", childBytes, err)
	}
	if childPID != pid {
		t.Fatalf("$! = %d, want external child PID %d", pid, childPID)
	}
	if pidLive(childPID) {
		t.Fatalf("external child PID %d survived carrier signal + wait", childPID)
	}
	waitPidsGone(t, c.startedPids())
}

// TestJobCarrierExternalTermAndKill checks the 128+signal status mapping
// for pure-builtin compound jobs killed externally: TERM -> 143 and
// KILL (uncatchable) -> 137.
func TestJobCarrierExternalTermAndKill(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, sig, want string
	}{
		{"Term", "TERM", "143"},
		{"Kill", "KILL", "137"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			c := new(testCarrier)
			out := runCarrierScript(t, c, killBinPrelude+`
{ while :; do :; done; } &
p=$!
"$K" -`+tc.sig+` "$p"
wait "$p"
echo "st=$?"
`)
			if got := strings.TrimSpace(out); got != "st="+tc.want {
				t.Fatalf("unexpected output:\n%s", out)
			}
			waitPidsGone(t, c.startedPids())
		})
	}
}

// TestJobCarrierTrappedTermRunsTrap checks that an external TERM aimed
// at the job's carrier PID is relayed into the job's trap machinery: the
// async list's own `trap ... TERM` action runs and controls the output
// and exit status, instead of the job dying as 143. The ready file makes
// the external kill deterministic — it only fires once the trap is
// installed.
func TestJobCarrierTrappedTermRunsTrap(t *testing.T) {
	t.Parallel()
	c := new(testCarrier)
	out := runCarrierScript(t, c, killBinPrelude+`
{ trap "echo trapped; exit 23" TERM; : >ready; while :; do :; done; } &
p=$!
while [ ! -e ready ]; do :; done
"$K" -TERM "$p"
wait "$p"
echo "st=$?"
`, interp.Dir(t.TempDir()))
	if got := strings.TrimSpace(out); got != "trapped\nst=23" {
		t.Fatalf("unexpected output:\n%s", out)
	}
	waitPidsGone(t, c.startedPids())
}

// TestJobCarrierIgnoredTermStaysAlive checks that an external TERM
// relayed to a job that ignores it (`trap ” TERM`) leaves the job
// running: it still completes its own work and exits with its own
// status. The kill-0 loop waits until the carrier is reaped, so the
// relay decision has been made before the job is released.
//
// Deliberately not parallel: `trap ” TERM` sets a process-wide real
// SIG_IGN (so exec'd children inherit it, as bash's children do), which
// a concurrently-started carrier in another test would inherit — its
// external `kill -TERM` would then do nothing. Sequential tests never
// overlap the parallel ones, and the deferred restore undoes the
// SIG_IGN before they run. signal.Reset alone is not enough: the Go
// runtime leaves the kernel disposition at SIG_IGN when resetting an
// Ignored signal, which would make later-started carriers inherit
// SIG_IGN (external TERM tests hang) and later-constructed runners see
// TERM as hard-ignored at startup. A Notify+Stop round-trip forces the
// runtime's own handler back in, so children inherit SIG_DFL again.
func TestJobCarrierIgnoredTermStaysAlive(t *testing.T) {
	defer func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGTERM)
		signal.Stop(ch)
	}()
	c := new(testCarrier)
	out := runCarrierScript(t, c, killBinPrelude+`
{ trap '' TERM; : >ready; until [ -e go ]; do :; done; echo alive; exit 5; } &
p=$!
while [ ! -e ready ]; do :; done
"$K" -TERM "$p"
while "$K" -0 "$p" 2>/dev/null; do :; done
: >go
wait "$p"
echo "st=$?"
`, interp.Dir(t.TempDir()))
	if got := strings.TrimSpace(out); got != "alive\nst=5" {
		t.Fatalf("unexpected output:\n%s", out)
	}
	waitPidsGone(t, c.startedPids())
}

// TestJobCarrierUncatchableKill checks that SIGKILL terminates the job
// as 137 even when the script recorded a trap for it: KILL is
// uncatchable, so the relay must never route it into the trap
// machinery.
func TestJobCarrierUncatchableKill(t *testing.T) {
	t.Parallel()
	c := new(testCarrier)
	out := runCarrierScript(t, c, killBinPrelude+`
{ trap "echo nope" KILL; : >ready; while :; do :; done; } &
p=$!
while [ ! -e ready ]; do :; done
"$K" -KILL "$p"
wait "$p"
echo "st=$?"
`, interp.Dir(t.TempDir()))
	if got := strings.TrimSpace(out); got != "st=137" {
		t.Fatalf("unexpected output:\n%s", out)
	}
	waitPidsGone(t, c.startedPids())
}

// TestJobCarrierConcurrentJobs starts several concurrent pure-builtin
// jobs, checks their carrier PIDs are unique real processes, then kills
// them all externally at once and checks every wait reports 143. Also
// serves as a race check for concurrent relay/teardown (run under -race
// in CI).
func TestJobCarrierConcurrentJobs(t *testing.T) {
	t.Parallel()
	c := new(testCarrier)
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	r, err := interp.New(interp.WithJobCarrier(c), interp.StdIO(nil, pw, pw))
	if err != nil {
		t.Fatal(err)
	}
	file := parse(t, nil, `
{ while :; do :; done; } & a=$!
{ while :; do :; done; } & b=$!
{ while :; do :; done; } & c=$!
{ while :; do :; done; } & d=$!
echo "$a $b $c $d"
wait "$a"; echo "$?"
wait "$b"; echo "$?"
wait "$c"; echo "$?"
wait "$d"; echo "$?"
`)
	ctx, cancel := context.WithTimeout(context.Background(), runnerRunTimeout)
	defer cancel()
	runErr := make(chan error, 1)
	go func() {
		defer pw.Close()
		runErr <- r.Run(ctx, file)
	}()
	br := bufio.NewReader(pr)
	first, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("reading pids: %v", err)
	}
	pids := strings.Fields(first)
	if len(pids) != 4 {
		t.Fatalf("want 4 pids, got %q", first)
	}
	seen := make(map[string]bool) // bashpp-racegate:safe-private
	var wg sync.WaitGroup
	for _, pid := range pids {
		if !numericPidRe.MatchString(pid) {
			t.Fatalf("$! = %q, want a real numeric PID", pid)
		}
		if seen[pid] {
			t.Fatalf("duplicate concurrent $! %s", pid)
		}
		seen[pid] = true
		n, _ := strconv.Atoi(pid)
		if !pidLive(n) {
			t.Fatalf("pid %d not alive while its job runs", n)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = syscall.Kill(n, syscall.SIGTERM)
		}()
	}
	wg.Wait()
	for i := range 4 {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("reading status %d: %v", i, err)
		}
		if got := strings.TrimSpace(line); got != "143" {
			t.Fatalf("wait #%d = %q, want 143", i, got)
		}
	}
	if err := <-runErr; err != nil {
		t.Fatalf("Run: %v", err)
	}
	waitPidsGone(t, c.startedPids())
}

// TestJobCarrierCancelReapsCarrier checks that canceling the runner's
// context tears the whole job down: the goroutine job, its external
// child, and the carrier process — no leaks.
func TestJobCarrierCancelReapsCarrier(t *testing.T) {
	t.Parallel()
	c := new(testCarrier)
	out := newLineObserver(8)
	r, err := interp.New(interp.WithJobCarrier(c), interp.StdIO(nil, out, out))
	if err != nil {
		t.Fatal(err)
	}
	file := parse(t, nil, `{ sleep 30; } & echo "$!"; wait "$!"; echo after`)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() {
		runDone <- r.Run(ctx, file)
	}()
	line := out.ReadLine(t, runnerRunTimeout)
	pid, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatalf("parsing pid %q: %v", line, err)
	}
	if !pidLive(pid) {
		t.Fatalf("carrier pid %d not alive while its job runs", pid)
	}
	cancel()
	select {
	case runErr := <-runDone:
		if runErr != nil && !errors.Is(runErr, context.Canceled) {
			t.Fatalf("Run after cancellation: %v", runErr)
		}
	case <-time.After(runnerRunTimeout):
		t.Fatalf("Run did not return after context cancellation; output=%q", out.String())
	}
	if err := out.Err(); err != nil {
		t.Fatalf("output observer: %v", err)
	}
	waitPidsGone(t, c.startedPids())
}
