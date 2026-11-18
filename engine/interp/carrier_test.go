// Copyright (c) 2026, the bashy authors
// See LICENSE for licensing information

package interp_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/interp"
)

// testCarrier implements [interp.JobCarrier] by re-execing the test
// binary with GOSH_CMD=carrier (see TestMain), a hermetic helper that
// blocks reading stdin until EOF. Terminate closes the stdin pipe and
// kills the process; Wait reaps it and maps a signal death to its number
// via carrierExitSignal (build-tagged, 0 on non-unix).
type testCarrier struct {
	mu        sync.Mutex
	pids      []int // every carrier PID handed out, for leak checks
	configure func(*exec.Cmd)
	wrap      func(*testCarrierProc) interp.CarrierProcess
}

type testCarrierProc struct {
	cmd   *exec.Cmd
	stdin io.Closer
}

type ignoredSignalCarrier struct {
	*testCarrier
	snapshots chan []string
}

func (c *ignoredSignalCarrier) StartCarrierWithIgnoredSignals(ctx context.Context, ignored []string) (interp.CarrierProcess, error) {
	c.snapshots <- append([]string(nil), ignored...)
	return c.testCarrier.StartCarrier(ctx)
}

func (c *testCarrier) StartCarrier(ctx context.Context) (interp.CarrierProcess, error) {
	cmd := exec.Command(os.Getenv("GOSH_PROG"))
	cmd.Env = append(os.Environ(), "GOSH_CMD=carrier")
	if c.configure != nil {
		c.configure(cmd)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	ready, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		stdin.Close()
		return nil, err
	}
	var marker [1]byte
	_, readyErr := io.ReadFull(ready, marker[:])
	_ = ready.Close()
	if readyErr != nil || marker[0] != 'R' {
		stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if readyErr != nil {
			return nil, readyErr
		}
		return nil, errors.New("invalid carrier readiness marker")
	}
	c.mu.Lock()
	c.pids = append(c.pids, cmd.Process.Pid)
	c.mu.Unlock()
	proc := &testCarrierProc{cmd: cmd, stdin: stdin}
	if c.wrap != nil {
		return c.wrap(proc), nil
	}
	return proc, nil
}

func (p *testCarrierProc) Pid() int { return p.cmd.Process.Pid }

func (p *testCarrierProc) Wait() int {
	_ = p.cmd.Wait() // a signal death surfaces as *exec.ExitError; state below has the detail
	return carrierExitSignal(p.cmd.ProcessState)
}

func (p *testCarrierProc) Terminate() {
	p.stdin.Close()
	_ = p.cmd.Process.Kill()
}

// startedPids returns a snapshot of every carrier PID started so far.
func (c *testCarrier) startedPids() []int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]int(nil), c.pids...)
}

// runCarrierScript runs src to completion on a fresh runner configured
// with the given carrier and returns the combined stdout+stderr.
func runCarrierScript(t *testing.T, carrier interp.JobCarrier, src string, opts ...interp.RunnerOption) string {
	t.Helper()
	file := parse(t, nil, src)
	var buf concBuffer
	if carrier != nil {
		opts = append(opts, interp.WithJobCarrier(carrier))
	}
	opts = append(opts, interp.StdIO(nil, &buf, &buf))
	r, err := interp.New(opts...)
	if err != nil {
		t.Fatal(err)
	}
	// This helper owns a terminal runner: callers only receive its completed
	// output, never the Runner for incremental reuse. Reset releases any OS
	// signal subscriptions installed by the script before the helper returns.
	defer r.Reset()
	ctx, cancel := context.WithTimeout(context.Background(), runnerRunTimeout)
	defer cancel()
	if err := r.Run(ctx, file); err != nil {
		var es interp.ExitStatus
		if !errors.As(err, &es) {
			t.Fatalf("Run: %v\noutput:\n%s", err, buf.String())
		}
	}
	return buf.String()
}

var numericPidRe = regexp.MustCompile(`^[1-9][0-9]*$`)

// TestJobCarrierIdentity checks that the carrier PID is one numeric
// identity used consistently by $!, jobs -p, wait, and wait -p, even for
// a job that never spawns an external process.
func TestJobCarrierIdentity(t *testing.T) {
	t.Parallel()
	c := new(testCarrier)
	out := runCarrierScript(t, c, `
{ :; } &
p=$!
jp=$(jobs -p)
jl=$(jobs -l)
wait -p w "$p"
echo "st=$?"
[ "$jp" = "$p" ] && echo jobs-ok
case "$jl" in *" $p "*) echo jobsl-ok;; esac
[ "$w" = "$p" ] && echo wait-ok
echo "pid=$p"
`)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	want := []string{"st=0", "jobs-ok", "jobsl-ok", "wait-ok"}
	if len(lines) != 5 || lines[0] != want[0] || lines[1] != want[1] ||
		lines[2] != want[2] || lines[3] != want[3] {
		t.Fatalf("unexpected output:\n%s", out)
	}
	pid := strings.TrimPrefix(lines[4], "pid=")
	if !numericPidRe.MatchString(pid) {
		t.Fatalf("$! = %q, want a real numeric PID", pid)
	}
	started := c.startedPids()
	if len(started) != 1 || fmt.Sprint(started[0]) != pid {
		t.Fatalf("$! = %s, but carrier started pids %v", pid, started)
	}
}

// TestJobCarrierRapidUniqueness starts jobs back to back and checks that
// every $! is a distinct real PID.
func TestJobCarrierRapidUniqueness(t *testing.T) {
	t.Parallel()
	out := runCarrierScript(t, new(testCarrier), `
for i in 1 2 3 4 5 6 7 8; do
	{ :; } &
	echo $!
done
wait
`)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 8 {
		t.Fatalf("want 8 pids, got:\n%s", out)
	}
	seen := make(map[string]bool) // bashpp-racegate:safe-private
	for _, pid := range lines {
		if !numericPidRe.MatchString(pid) {
			t.Fatalf("$! = %q, want a real numeric PID", pid)
		}
		if seen[pid] {
			t.Fatalf("duplicate $! %s in:\n%s", pid, out)
		}
		seen[pid] = true
	}
}

// TestJobCarrierStatusRetention checks that a finished job's status is
// retained: `wait $p` reports it once — including after a bare `wait`
// has reaped the job table, via the saved-status map — and a second
// `wait $p` gets bash's "not a child" 127.
func TestJobCarrierStatusRetention(t *testing.T) {
	t.Parallel()
	out := runCarrierScript(t, new(testCarrier), `
{ exit 7; } &
p=$!
wait
wait "$p"
echo "one=$?"
wait "$p" 2>/dev/null
echo "two=$?"
`)
	if got := strings.TrimSpace(out); got != "one=7\ntwo=127" {
		t.Fatalf("unexpected output:\n%s", out)
	}
}

// TestJobCarrierSubshellIsolation checks that a subshell sees the
// parent's $! (the carrier PID) but cannot wait on the parent's job.
func TestJobCarrierSubshellIsolation(t *testing.T) {
	t.Parallel()
	out := runCarrierScript(t, new(testCarrier), `
{ :; } &
p=$!
( [ "$!" = "$p" ] && echo inherit-ok
  wait "$p" 2>/dev/null
  echo "sub=$?" )
wait "$p"
echo "par=$?"
`)
	if got := strings.TrimSpace(out); got != "inherit-ok\nsub=127\npar=0" {
		t.Fatalf("unexpected output:\n%s", out)
	}
}

// TestJobCarrierCustomExecHandler checks that carrier identity does not
// depend on the default exec handler: a job whose commands are entirely
// satisfied by a custom ExecHandlers middleware still gets a real PID.
func TestJobCarrierCustomExecHandler(t *testing.T) {
	t.Parallel()
	handled := func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
		return func(ctx context.Context, args []string) error {
			if args[0] == "frobnicate" {
				return nil // satisfied in-process; no OS process spawned
			}
			return next(ctx, args)
		}
	}
	out := runCarrierScript(t, new(testCarrier), `
{ frobnicate; exit 3; } &
p=$!
wait "$p"
echo "st=$?"
echo "pid=$p"
`, interp.ExecHandlers(handled))
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 || lines[0] != "st=3" {
		t.Fatalf("unexpected output:\n%s", out)
	}
	if pid := strings.TrimPrefix(lines[1], "pid="); !numericPidRe.MatchString(pid) {
		t.Fatalf("$! = %q, want a real numeric PID", pid)
	}
}

// TestJobCarrierReset checks that the carrier option survives
// [Runner.Reset] and that a reset runner hands out fresh identities.
func TestJobCarrierReset(t *testing.T) {
	t.Parallel()
	c := new(testCarrier)
	var buf concBuffer
	r, err := interp.New(interp.WithJobCarrier(c), interp.StdIO(nil, &buf, &buf))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), runnerRunTimeout)
	defer cancel()
	src := "{ :; } & echo $!; wait $!"
	if err := r.Run(ctx, parse(t, nil, src)); err != nil {
		t.Fatal(err)
	}
	r.Reset()
	if err := r.Run(ctx, parse(t, nil, src)); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("unexpected output:\n%s", buf.String())
	}
	for _, pid := range lines {
		if !numericPidRe.MatchString(pid) {
			t.Fatalf("$! = %q, want a real numeric PID", pid)
		}
	}
	if lines[0] == lines[1] {
		t.Fatalf("same carrier PID handed out before and after Reset: %s", lines[0])
	}
}

// failingCarrier always errors from StartCarrier.
type failingCarrier struct{}

func (failingCarrier) StartCarrier(ctx context.Context) (interp.CarrierProcess, error) {
	return nil, errors.New("carrier unavailable")
}

// TestJobCarrierStartFailure checks that the seam fails closed: with a
// carrier configured, a StartCarrier error must not degrade to the
// synthetic g<N> identity. The job never runs, the statement fails with
// a diagnostic and $? = 1, and $! keeps its previous (unset) value.
func TestJobCarrierStartFailure(t *testing.T) {
	t.Parallel()
	out := runCarrierScript(t, failingCarrier{}, `
{ echo ran; exit 5; } &
echo "st=$?"
[ -z "$!" ] && echo no-bang
wait
echo "wait=$?"
`)
	if strings.Contains(out, "ran") {
		t.Fatalf("job ran despite carrier failure:\n%s", out)
	}
	if !strings.Contains(out, "carrier unavailable") {
		t.Fatalf("missing carrier diagnostic:\n%s", out)
	}
	for _, want := range []string{"st=1", "no-bang", "wait=0"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in output:\n%s", want, out)
		}
	}
}

// badPidCarrier hands out an in-process fake carrier whose Pid is
// nonpositive, recording whether the runner reaps it.
type badPidCarrier struct {
	terminated chan struct{}
	waited     chan struct{}
	once       sync.Once
}

type badPidProc struct{ c *badPidCarrier }

func (c *badPidCarrier) StartCarrier(ctx context.Context) (interp.CarrierProcess, error) {
	return badPidProc{c}, nil
}

func (p badPidProc) Pid() int { return 0 }

func (p badPidProc) Wait() int {
	<-p.c.terminated
	close(p.c.waited)
	return 0
}

func (p badPidProc) Terminate() {
	p.c.once.Do(func() { close(p.c.terminated) })
}

// TestJobCarrierBadPid checks that a carrier reporting a nonpositive
// PID also fails the job closed — no synthetic identity, nonzero
// status, diagnostic — and that the useless carrier is still reaped
// (Terminate then Wait).
func TestJobCarrierBadPid(t *testing.T) {
	t.Parallel()
	c := &badPidCarrier{
		terminated: make(chan struct{}),
		waited:     make(chan struct{}),
	}
	out := runCarrierScript(t, c, `
{ echo ran; } &
echo "st=$?"
[ -z "$!" ] && echo no-bang
`)
	if strings.Contains(out, "ran") {
		t.Fatalf("job ran despite bad carrier pid:\n%s", out)
	}
	if !strings.Contains(out, "invalid carrier pid 0") {
		t.Fatalf("missing carrier diagnostic:\n%s", out)
	}
	for _, want := range []string{"st=1", "no-bang"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in output:\n%s", want, out)
		}
	}
	select {
	case <-c.waited:
	case <-time.After(runnerRunTimeout):
		t.Fatal("bad-pid carrier was not reaped")
	}
}

// deadCarrier hands out a fake carrier that is already dead of the
// given signal: Wait returns the signal number immediately. It models
// an external kill landing before the background runner is ready.
type deadCarrier struct{ sig int }

type deadCarrierProc struct{ sig int }

func (c deadCarrier) StartCarrier(ctx context.Context) (interp.CarrierProcess, error) {
	return deadCarrierProc{c.sig}, nil
}

func (p deadCarrierProc) Pid() int   { return os.Getpid() }
func (p deadCarrierProc) Wait() int  { return p.sig }
func (p deadCarrierProc) Terminate() {}

// TestJobCarrierPreReadyTrappedSignal checks that a catchable signal
// relayed before the background runner has run a single statement is
// still delivered through the pending-signal machinery: the job's
// inherited TERM trap runs and controls the exit status, instead of the
// job dying as 128+15.
func TestJobCarrierPreReadyTrappedSignal(t *testing.T) {
	t.Parallel()
	out := runCarrierScript(t, deadCarrier{sig: 15}, `
trap "echo trapped; exit 23" TERM
{ while :; do :; done; } &
wait "$!"
echo "st=$?"
`)
	if got := strings.TrimSpace(out); got != "trapped\nst=23" {
		t.Fatalf("unexpected output:\n%s", out)
	}
}

// reapOrderCarrier hands out an in-process fake carrier whose Wait
// return — the moment the OS considers the carrier reaped — is gated by
// the test. It models a carrier that exits promptly on Terminate but is
// only reaped a controllable instant later, so a test can pin the engine
// contract that `wait` on a naturally completed carrier-backed job does
// not unblock until the carrier has been fully reaped.
type reapOrderCarrier struct {
	terminated chan struct{} // closed by Terminate: the job asked the carrier to exit
	release    chan struct{} // closed by the test: lets Wait return (carrier reaped)
	waited     chan struct{} // closed once Wait has returned
	once       sync.Once
}

type reapOrderProc struct{ c *reapOrderCarrier }

func (c *reapOrderCarrier) StartCarrier(ctx context.Context) (interp.CarrierProcess, error) {
	return reapOrderProc{c}, nil
}

func (p reapOrderProc) Pid() int { return os.Getpid() }

func (p reapOrderProc) Wait() int {
	<-p.c.terminated
	<-p.c.release
	close(p.c.waited)
	return 0
}

func (p reapOrderProc) Terminate() {
	p.c.once.Do(func() { close(p.c.terminated) })
}

// TestJobCarrierWaitBlocksUntilReaped is the deterministic regression for
// the lifecycle race behind the TET wait loop: when the job completes
// naturally, `wait` must not return until the carrier has been Terminated
// AND reaped (its single Wait has returned). Otherwise the carrier
// lingers as a live PID after `wait` unblocks, a following `kill -0 $!`
// still sees it, and a retrying `wait` loop hits "pid is not a child".
func TestJobCarrierWaitBlocksUntilReaped(t *testing.T) {
	t.Parallel()
	c := &reapOrderCarrier{
		terminated: make(chan struct{}),
		release:    make(chan struct{}),
		waited:     make(chan struct{}),
	}
	done := make(chan string, 1)
	go func() {
		done <- runCarrierScript(t, c, "{ :; } & wait; echo reaped")
	}()

	// The job body runs instantly, so the runner Terminates the carrier
	// right away — but the reap (Wait's return) is still gated by us.
	select {
	case <-c.terminated:
	case <-time.After(runnerRunTimeout):
		t.Fatal("carrier was never terminated")
	}

	// Until we let Wait return, `wait` must stay blocked. If the script
	// finishes here, the engine unblocked `wait` while the carrier PID was
	// still alive — exactly the race this fix closes.
	select {
	case out := <-done:
		t.Fatalf("wait returned before the carrier was reaped; output: %q", out)
	case <-time.After(50 * time.Millisecond):
	}

	close(c.release) // allow the single Wait to return: carrier reaped

	select {
	case out := <-done:
		if got := strings.TrimSpace(out); got != "reaped" {
			t.Fatalf("unexpected output: %q", got)
		}
	case <-time.After(runnerRunTimeout):
		t.Fatal("wait never returned after the carrier was reaped")
	}
	// The reap strictly preceded wait's return, so waited is already closed.
	select {
	case <-c.waited:
	default:
		t.Fatal("carrier Wait did not complete before wait returned")
	}
}

type stopAwareProc struct {
	states     chan interp.CarrierWaitState
	terminated chan struct{}
	termOnce   sync.Once
}

func (p *stopAwareProc) Pid() int { return 4242 }
func (p *stopAwareProc) Wait() int {
	panic("Wait called on stop-aware carrier")
}
func (p *stopAwareProc) WaitState() interp.CarrierWaitState { return <-p.states }
func (p *stopAwareProc) Terminate() {
	p.termOnce.Do(func() { close(p.terminated) })
}

type stopAwareCarrier struct{ proc *stopAwareProc }

func (c stopAwareCarrier) StartCarrier(context.Context) (interp.CarrierProcess, error) {
	return c.proc, nil
}

type groupStopAwareProc struct {
	*stopAwareProc
	resumed chan struct{}
	once    sync.Once
}

func (p *groupStopAwareProc) ProcessGroupID() int { return p.Pid() }
func (p *groupStopAwareProc) ResumeProcessGroupLeader() error {
	p.once.Do(func() { close(p.resumed) })
	return nil
}

type groupStopAwareCarrier struct{ proc *groupStopAwareProc }

func (c groupStopAwareCarrier) StartCarrier(context.Context) (interp.CarrierProcess, error) {
	return c.proc, nil
}

func TestJobCarrierGroupStopPreservesJobForResume(t *testing.T) {
	base := &stopAwareProc{
		states:     make(chan interp.CarrierWaitState, 2),
		terminated: make(chan struct{}),
	}
	p := &groupStopAwareProc{stopAwareProc: base, resumed: make(chan struct{})}
	p.states <- interp.CarrierWaitState{Signal: 21, Stopped: true}
	runDone := make(chan string, 1)
	go func() {
		runDone <- runCarrierScript(t, groupStopAwareCarrier{p},
			`{ while :; do :; done; } & wait $!; echo "st=$?"`, interp.Params("-m"))
	}()
	select {
	case <-p.resumed:
	case <-time.After(time.Second):
		t.Fatal("grouped carrier proxy was not resumed after stop")
	}
	select {
	case <-p.terminated:
		t.Fatal("grouped carrier stop canceled the represented job")
	default:
	}
	p.states <- interp.CarrierWaitState{Signal: 15}
	select {
	case out := <-runDone:
		if got := strings.TrimSpace(out); got != "st=143" {
			t.Fatalf("unexpected output after terminal carrier state: %q", got)
		}
	case <-time.After(runnerRunTimeout):
		t.Fatal("grouped carrier job did not finish after terminal state")
	}
}

// TestJobCarrierStopCancelsBeforeTerminalReap pins the ed timeout regression:
// a host which observes a stopped carrier must not wait for an ordinary
// terminal-only process Wait while the represented job keeps running.
func TestJobCarrierStopCancelsBeforeTerminalReap(t *testing.T) {
	p := &stopAwareProc{
		states:     make(chan interp.CarrierWaitState, 2),
		terminated: make(chan struct{}),
	}
	p.states <- interp.CarrierWaitState{Signal: 20, Stopped: true}
	runDone := make(chan string, 1)
	go func() {
		runDone <- runCarrierScript(t, stopAwareCarrier{p}, `{ while :; do :; done; } & wait $!; echo "st=$?"`)
	}()
	select {
	case <-p.terminated:
	case <-time.After(time.Second):
		t.Fatal("stopped carrier did not cancel the job and request termination")
	}
	select {
	case out := <-runDone:
		t.Fatalf("wait returned before stopped carrier was terminally reaped: %q", out)
	case <-time.After(50 * time.Millisecond):
	}
	p.states <- interp.CarrierWaitState{}
	select {
	case out := <-runDone:
		if got := strings.TrimSpace(out); got != "st=148" {
			t.Fatalf("unexpected output: %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("wait did not return after stopped carrier was reaped")
	}
}

// TestJobCarrierWaitLoopNoRace runs the exact TET reproduction loop
// against the real re-exec carrier: once `wait` on a naturally completed
// job returns, `kill -0` must no longer see the carrier, so the loop
// terminates cleanly without ever emitting "not a child". Repeated to
// shake out any residual scheduling race.
func TestJobCarrierWaitLoopNoRace(t *testing.T) {
	t.Parallel()
	for i := range 20 {
		out := runCarrierScript(t, new(testCarrier), `
( : ) & p=$!
while kill -s 0 "$p" 2>/dev/null; do wait "$p"; done
echo done
`)
		if got := strings.TrimSpace(out); got != "done" {
			t.Fatalf("iteration %d: unexpected output:\n%s", i, out)
		}
	}
}

// TestNoJobCarrierKeepsSyntheticHandles pins the default behavior for
// embedders that do not opt in: pure-builtin jobs keep the opaque g<N>
// handle for $!, jobs -p, and wait.
func TestNoJobCarrierKeepsSyntheticHandles(t *testing.T) {
	t.Parallel()
	out := runCarrierScript(t, nil, `
{ :; } &
p=$!
jp=$(jobs -p)
[ "$jp" = "$p" ] && echo jobs-ok
wait "$p"
echo "st=$? pid=$p"
`)
	if got := strings.TrimSpace(out); got != "jobs-ok\nst=0 pid=g1" {
		t.Fatalf("unexpected output:\n%s", out)
	}
}
