// Copyright (c) 2026, the outpost authors
// See LICENSE for licensing information

//go:build unix

package interp

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// TestIsRuntimeSignalClassification verifies that only the five synchronous
// fault signals (BUS, FPE, ILL, SEGV, TRAP) are recognised as runtime-owned
// and that ordinary catchable signals are not.
func TestIsRuntimeSignalClassification(t *testing.T) {
	t.Parallel()

	runtime := map[string]bool{
		"BUS":  true,
		"FPE":  true,
		"ILL":  true,
		"SEGV": true,
		"TRAP": true,
	}
	safe := map[string]bool{
		"HUP":   false,
		"INT":   false,
		"QUIT":  false,
		"ABRT":  false,
		"USR1":  false,
		"USR2":  false,
		"PIPE":  false,
		"ALRM":  false,
		"TERM":  false,
		"CHLD":  false,
		"CONT":  false,
		"STOP":  false,
		"TSTP":  false,
		"TTIN":  false,
		"TTOU":  false,
		"URG":   false,
		"XCPU":  false,
		"XFSZ":  false,
		"WINCH": false,
		"KILL":  false,
	}

	for name, wantRuntime := range runtime {
		if got := isRuntimeSignal(name); got != wantRuntime {
			t.Errorf("isRuntimeSignal(%q) = %v, want %v", name, got, wantRuntime)
		}
	}
	for name, wantRuntime := range safe {
		if got := isRuntimeSignal(name); got != wantRuntime {
			t.Errorf("isRuntimeSignal(%q) = %v, want %v", name, got, wantRuntime)
		}
	}
}

// TestSynchronousFaultSignalTrappable verifies that the POSIX synchronous
// fault signals (BUS, FPE, ILL, SEGV, TRAP) are first-class trappable signals:
// enableSignalTrap installs a real signal.Notify handler, ignoreSignalTrap sets
// SIG_IGN, and disableSignalTrap tears a prior trap down. A pure-Go shell must
// catch a kill(2)-delivered instance via the handler (Go's runtime still
// recovers genuine CPU faults on its own), so that `trap '...' BUS; kill -BUS
// $$` runs the trap instead of faulting the interpreter (VSC-PCTS TP712/713/714).
// Reverts the e42746db guard that made these operations no-ops.
func TestSynchronousFaultSignalTrappable(t *testing.T) {
	// These calls install real OS dispositions for the test process, so do
	// not run them concurrently with other signal tests.
	guardRuntimeSignalDispositions(t)
	runtimeNames := []string{"BUS", "FPE", "ILL", "SEGV", "TRAP"}

	for _, name := range runtimeNames {
		// enableSignalTrap installs a signal.Notify handler.
		r := &Runner{}
		r.enableSignalTrap(name)
		t.Cleanup(r.stopSignalSubscriptions)
		r.sigMu.Lock()
		enabled := r.sigNotifyCh[name]
		_, hasNotify := r.sigNotify[name]
		r.sigMu.Unlock()
		if !hasNotify {
			t.Errorf("enableSignalTrap(%q) did not install a signal.Notify handler", name)
		}

		// Ignore the actual subscription above, joining its worker before
		// replacing its OS disposition with SIG_IGN.
		r.ignoreSignalTrap(name)
		select {
		case <-enabled.finished:
		default:
			t.Errorf("ignoreSignalTrap(%q) left the prior subscription running", name)
		}
		r.sigMu.Lock()
		_, hasIgnore := r.sigIgnored[name]
		r.sigMu.Unlock()
		if !hasIgnore {
			t.Errorf("ignoreSignalTrap(%q) did not set SIG_IGN", name)
		}
		if !osSignalIgnored(signalForOS(signalByNameMust(name))) {
			t.Errorf("ignoreSignalTrap(%q) did not install OS SIG_IGN", name)
		}

		// Re-enable and disable a real subscription, rather than fabricating
		// metadata that cannot demonstrate ownership of a forwarding worker.
		r.enableSignalTrap(name)
		r.sigMu.Lock()
		enabled = r.sigNotifyCh[name]
		r.sigMu.Unlock()
		r.disableSignalTrap(name)
		select {
		case <-enabled.finished:
		default:
			t.Errorf("disableSignalTrap(%q) left the prior subscription running", name)
		}
		r.sigMu.Lock()
		_, stillHasNotify := r.sigNotify[name]
		r.sigMu.Unlock()
		if stillHasNotify {
			t.Errorf("disableSignalTrap(%q) did not remove the handler", name)
		}
	}
}

// TestSignalSubscriptionDrainKeepsQueuedDeliveryCallbacks exercises teardown
// under a queue larger than the old central delivery buffer. A transition must
// neither deadlock on sigMu nor lose/reclassify deliveries already accepted by
// the old subscription.
func TestSignalSubscriptionDrainKeepsQueuedDeliveryCallbacks(t *testing.T) {
	const deliveries = 256
	r := &Runner{}
	r.sigMu.Lock()
	r.ensureSignalLoopLocked()
	sub := signalSubscription{
		ch:       make(chan os.Signal, deliveries),
		done:     make(chan struct{}),
		finished: make(chan struct{}),
		callback: "old action",
	}
	r.sigNotifyCh = map[string]signalSubscription{"USR1": sub}
	r.sigMu.Unlock()
	for range deliveries {
		sub.ch <- syscall.SIGUSR1
	}
	go r.forwardSignalSubscription("USR1", sub)

	r.sigMu.Lock()
	stopped := r.stopSignalSubscriptionLocked("USR1")
	r.sigMu.Unlock()
	r.waitSignalSubscription(stopped)

	for i := range deliveries {
		name, callback := r.nextPendingSignal()
		if name != "USR1" || callback != "old action" {
			t.Fatalf("delivery %d = (%q, %q), want (USR1, old action)", i, name, callback)
		}
	}
	if name, callback := r.nextPendingSignal(); name != "" || callback != "" {
		t.Fatalf("extra delivery = (%q, %q)", name, callback)
	}
}

// TestRunnerResetStopsSignalSubscriptions verifies the ownership boundary for
// per-run signal forwarders. A Runner is commonly returned to a pool after a
// normal return, shell exit, or context cancellation; Reset must join every
// old forwarder before replacing the Runner value, on every reuse cycle.
func TestRunnerResetStopsSignalSubscriptions(t *testing.T) {
	tests := []struct {
		name string
		src  string
		ctx  func() (context.Context, context.CancelFunc)
	}{
		{
			name: "return",
			src:  ":",
			ctx: func() (context.Context, context.CancelFunc) {
				return context.WithCancel(context.Background())
			},
		},
		{
			name: "exit",
			src:  "exit 7",
			ctx: func() (context.Context, context.CancelFunc) {
				return context.WithCancel(context.Background())
			},
		},
		{
			name: "cancel",
			src:  "while :; do :; done",
			ctx: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx, func() {}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r, err := New()
			if err != nil {
				t.Fatal(err)
			}
			file, err := syntax.NewParser().Parse(strings.NewReader(tc.src), "")
			if err != nil {
				t.Fatal(err)
			}
			trapFile, err := syntax.NewParser().Parse(strings.NewReader("trap ':' USR1"), "")
			if err != nil {
				t.Fatal(err)
			}
			for cycle := 0; cycle < 4; cycle++ {
				if err := r.Run(context.Background(), trapFile); err != nil {
					t.Fatalf("install trap: %v", err)
				}
				ctx, cancel := tc.ctx()
				_ = r.Run(ctx, file)
				cancel()

				r.sigMu.Lock()
				sub, ok := r.sigNotifyCh["USR1"]
				r.sigMu.Unlock()
				if !ok {
					t.Fatal("USR1 subscription was not installed")
				}

				r.Reset()
				select {
				case <-sub.finished:
				case <-time.After(time.Second):
					t.Fatal("signal forwarder survived Runner.Reset")
				}
			}
		})
	}
}

// TestParenthesizedShellStopsSignalSubscriptions covers the inner shell of
// both `( ... )` and `( ... ) &`. Its trap workers belong to that terminal
// scope, including traps installed by its EXIT handler; the outer Runner
// remains incrementally reusable and retains its own subscriptions.
func TestParenthesizedShellStopsSignalSubscriptions(t *testing.T) {
	for _, suffix := range []string{"", " & wait"} {
		t.Run("subshell"+suffix, func(t *testing.T) {
			type observedSubscription struct {
				owner *Runner
				sub   signalSubscription
			}
			observed := make(chan observedSubscription, 2)
			r, err := New(ExecHandlers(func(next ExecHandlerFunc) ExecHandlerFunc {
				return func(ctx context.Context, args []string) error {
					if args[0] != "capture-signal" {
						return next(ctx, args)
					}
					owner := HandlerCtx(ctx).runner
					owner.sigMu.Lock()
					sub := owner.sigNotifyCh[args[1]]
					owner.sigMu.Unlock()
					observed <- observedSubscription{owner, sub}
					return nil
				}
			}))
			if err != nil {
				t.Fatal(err)
			}
			defer r.Reset()
			run := func(source string) {
				t.Helper()
				file, err := syntax.NewParser().Parse(strings.NewReader(source), "")
				if err != nil {
					t.Fatal(err)
				}
				if err := r.Run(t.Context(), file); err != nil {
					t.Fatal(err)
				}
			}
			run("trap ':' USR1")
			parent := r.sigNotifyCh["USR1"]
			run(`(trap 'echo WRONG-SENDER' TERM; capture-signal TERM; trap 'trap : USR2; capture-signal USR2' EXIT)` + suffix)
			if len(observed) != 2 {
				t.Fatalf("observed %d subscriptions, want body and EXIT subscriptions", len(observed))
			}
			for range 2 {
				got := <-observed
				defer got.owner.stopSignalSubscriptions() // Keep a failed assertion isolated.
				if got.owner == r || got.sub.finished == nil {
					t.Fatal("did not observe an inner shell's owned subscription")
				}
				select {
				case <-got.sub.finished:
				default:
					t.Error("completed parenthesized shell retained a signal forwarder")
				}
			}
			select {
			case <-parent.finished:
				t.Fatal("inner shell completion stopped the outer Runner's subscription")
			default:
			}
		})
	}
}

// TestBackgroundRunnerStopsSignalSubscriptions verifies the other ownership
// boundary: an async-list runner is an internal terminal subshell, not an
// incrementally reusable Runner. A trap installed inside that list must be
// joined when its background job completes, while the parent's own trap state
// remains available for later incremental Runs.
func TestBackgroundRunnerStopsSignalSubscriptions(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	returned := make(chan struct{})
	r, err := New(ExecHandlers(func(next ExecHandlerFunc) ExecHandlerFunc {
		return func(ctx context.Context, args []string) error {
			if args[0] != "block" {
				return next(ctx, args)
			}
			close(started)
			<-release
			close(returned)
			return nil
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	file, err := syntax.NewParser().Parse(strings.NewReader(`
trap ':' TERM
{ trap ':' USR1; block; } &
`), "")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), file); err != nil {
		t.Fatal(err)
	}
	if len(r.bgProcs) != 1 {
		t.Fatalf("background jobs = %d, want 1", len(r.bgProcs))
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("background job did not start")
	}
	child := r.bgProcs[0].carrierSignalRunner.Load()
	if child == nil {
		t.Fatal("background job has no signal runner")
	}
	child.sigMu.Lock()
	sub, ok := child.sigNotifyCh["USR1"]
	if !ok {
		child.sigMu.Unlock()
		t.Fatal("background runner did not install USR1 subscription")
	}
	close(release)
	<-returned
	select {
	case <-r.bgProcs[0].done:
		child.sigMu.Unlock()
		t.Fatal("background completion published before signal cleanup")
	case <-time.After(50 * time.Millisecond):
	}
	child.sigMu.Unlock()
	select {
	case <-r.bgProcs[0].done:
	case <-time.After(time.Second):
		t.Fatal("background job did not complete after signal cleanup unblocked")
	}
	select {
	case <-sub.finished:
	default:
		t.Fatal("background completion published before signal forwarder joined")
	}
	child.sigMu.Lock()
	childSubs := len(child.sigNotifyCh)
	child.sigMu.Unlock()
	if childSubs != 0 {
		t.Fatalf("terminal background runner retained %d signal subscription(s)", childSubs)
	}

	// The parent remains incrementally reusable; its trap was not torn down
	// merely because a child job ended.
	r.sigMu.Lock()
	_, parentHasTERM := r.sigNotifyCh["TERM"]
	r.sigMu.Unlock()
	if !parentHasTERM {
		t.Fatal("background cleanup removed the parent's incremental TERM trap")
	}
	r.Reset()
}

// TestUncatchableSignalTrapsDoNotSubscribe verifies that bash-compatible trap
// metadata for KILL and STOP does not create impossible OS subscriptions or
// ignored dispositions. Resetting such metadata must remain a no-op too.
func TestUncatchableSignalTrapsDoNotSubscribe(t *testing.T) {
	for _, name := range []string{"KILL", "STOP"} {
		t.Run(name, func(t *testing.T) {
			r := &Runner{trapCallbacks: map[string]string{name: ":"}}
			r.enableSignalTrap(name)
			if got := r.trapCallbacks[name]; got != ":" {
				t.Fatalf("trap action = %q, want preserved metadata", got)
			}
			r.trapCallbacks[name] = ""
			r.ignoreSignalTrap(name)
			if got := r.trapCallbacks[name]; got != "" {
				t.Fatalf("ignore action = %q, want preserved metadata", got)
			}
			r.disableSignalTrap(name)
			if len(r.sigNotifyCh) != 0 {
				t.Fatalf("uncatchable signal created %d subscription(s)", len(r.sigNotifyCh))
			}
			if _, ok := r.sigNotify[name]; ok {
				t.Fatal("uncatchable signal recorded as notified")
			}
			if _, ok := r.sigIgnored[name]; ok {
				t.Fatal("uncatchable signal recorded as ignored")
			}
		})
	}
}

// TestRestoreBridgedStartupIgnoresRuntimeSignals verifies that a synchronous
// fault signal (BUS/FPE/ILL/SEGV/TRAP) marked SIG_IGN on entry is reinstalled
// as a real OS-level SIG_IGN, so a later kill(2)/SI_USER delivery is discarded
// by the kernel rather than reaching Go's runtime fault handler and aborting
// the process with a traceback (VSC-PCTS TP720). It only sets and probes
// dispositions; it never raises these signals.
func TestRestoreBridgedStartupIgnoresRuntimeSignals(t *testing.T) {
	// This sets real OS dispositions for the test process, so do not run it
	// concurrently with other signal tests.
	guardRuntimeSignalDispositions(t)
	runtimeNames := []string{"BUS", "FPE", "ILL", "SEGV", "TRAP"}

	for _, name := range runtimeNames {
		r := &Runner{
			sigReset:       OSSignalResetter{},
			startupIgnored: map[string]bool{name: true},
		}
		r.restoreBridgedStartupIgnores()
		if !osSignalIgnored(signalForOS(signalByNameMust(name))) {
			t.Errorf("restoreBridgedStartupIgnores did not install OS SIG_IGN for %q", name)
		}
	}
}

// guardRuntimeSignalDispositions snapshots the OS dispositions of the five
// synchronous fault signals and restores them when the test finishes. A test
// that installs a real OS SIG_IGN for these signals must call this: otherwise
// the leaked disposition is later sampled by a fresh Runner into
// startupIgnored and corrupts unrelated tests (e.g. TestTrapPrint*). On
// platforms without raw-sigaction save support the snapshot is a no-op.
func guardRuntimeSignalDispositions(t *testing.T) {
	for _, name := range []string{"BUS", "FPE", "ILL", "SEGV", "TRAP"} {
		sig := signalForOS(signalByNameMust(name))
		if disp, ok := saveSignalDisposition(sig); ok {
			t.Cleanup(func() { restoreSignalDisposition(sig, disp) })
		}
	}
}

func signalByNameMust(name string) killSig {
	sig, ok := signalByName(name)
	if !ok {
		panic("missing test signal: " + name)
	}
	return sig
}

// TestRuntimeSignalWithSignalResetterListBounds verifies that none of the
// five runtime-owned signals appear in the WithSignalResetter default-reset
// list. The list is the only code path that calls restoreExecSignal at
// construction time, so its contents are the entire OS-impact surface for a
// standalone shell on startup.
func TestRuntimeSignalWithSignalResetterListBounds(t *testing.T) {
	t.Parallel()

	// This is a static-assert-style test: we walk the same literal slice
	// that WithSignalResetter iterates and verify no runtime name appears.
	resetList := [...]string{
		"HUP", "INT", "QUIT", "ABRT",
		"USR1", "USR2", "PIPE", "ALRM", "TERM",
		"TSTP", "TTIN", "TTOU", "XCPU", "XFSZ",
	}
	inList := make(map[string]bool)
	for _, name := range resetList {
		inList[name] = true
	}
	runtimeNames := []string{"BUS", "FPE", "ILL", "SEGV", "TRAP"}
	for _, name := range runtimeNames {
		if inList[name] {
			t.Errorf("runtime-owned signal %q must not appear in the WithSignalResetter reset list", name)
		}
	}
	// Additionally verify that CHLD and URG — the two other runtime-managed
	// signals — are absent, as they have their own explicit guards.
	for _, name := range []string{"CHLD", "URG"} {
		if inList[name] {
			t.Errorf("runtime-managed signal %q must not appear in the WithSignalResetter reset list", name)
		}
	}
}

func TestStandaloneRuntimeSignalDefaults(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux runtime signal relay")
	}
	const helperEnv = "SH_RUNTIME_DEFAULT_HELPER"
	if name := os.Getenv(helperEnv); name != "" {
		_, err := New(
			Env(nil),
			WithSignalResetter(OSSignalResetter{}),
			WithStandaloneSignalDefaults(),
		)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = os.Stdout.WriteString("ready\n")
		select {}
	}

	for _, name := range []string{"BUS", "FPE", "ILL", "SEGV", "TRAP"} {
		t.Run(name, func(t *testing.T) {
			sig := signalByNameMust(name)
			cmd := exec.Command(os.Args[0], "-test.run=^TestStandaloneRuntimeSignalDefaults$")
			cmd.Env = append(os.Environ(), "GOSH_PROG=", helperEnv+"="+name)
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			var stderr strings.Builder // bashpp-racegate:safe-private
			cmd.Stderr = &stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "ready\n" {
				waitErr := cmd.Wait()
				t.Fatalf("helper readiness = %q, %v; wait=%v stderr=%q", line, err, waitErr, stderr.String())
			}
			if err := cmd.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			err = cmd.Wait()
			exitErr, ok := err.(*exec.ExitError)
			if !ok {
				t.Fatalf("wait error = %v, want signal death", err)
			}
			status, ok := exitErr.Sys().(syscall.WaitStatus)
			if !ok || !status.Signaled() || status.Signal() != sig {
				t.Fatalf("wait status = %#v, want signal %s; stderr=%q", exitErr.Sys(), name, stderr.String())
			}
			if stderr.Len() != 0 {
				t.Fatalf("signal default emitted diagnostics: %q", stderr.String())
			}
		})
	}
}

func TestStandaloneRuntimeSignalInheritedIgnore(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux runtime signal relay")
	}
	const helperEnv = "SH_RUNTIME_IGNORE_HELPER"
	if name := os.Getenv(helperEnv); name != "" {
		r, err := New(
			Env(nil),
			WithSignalResetter(OSSignalResetter{}),
			WithStandaloneSignalDefaults(),
		)
		if err != nil {
			t.Fatal(err)
		}
		// Run forces the initial Reset after the standalone option has
		// installed its default-action subscriptions. The Reset must preserve
		// the bridged SIG_IGN instead of recreating a signal.Notify relay that
		// overwrites it. Do this before announcing readiness so the parent can
		// only signal us after crossing the real Runner lifecycle boundary.
		file, err := syntax.NewParser().Parse(strings.NewReader(":"), "")
		if err != nil {
			t.Fatal(err)
		}
		if err := r.Run(context.Background(), file); err != nil {
			t.Fatal(err)
		}
		_, _ = os.Stdout.WriteString("ready\n")
		select {}
	}

	for _, name := range []string{"BUS", "FPE", "ILL", "SEGV"} {
		t.Run(name, func(t *testing.T) {
			sig := signalByNameMust(name)
			cmd := exec.Command(os.Args[0], "-test.run=^TestStandaloneRuntimeSignalInheritedIgnore$")
			cmd.Env = append(os.Environ(), "GOSH_PROG=", helperEnv+"="+name, BashyHardIgnoreEnv+"="+name)
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			if line, err := bufio.NewReader(stdout).ReadString('\n'); err != nil || line != "ready\n" {
				waitErr := cmd.Wait()
				t.Fatalf("helper readiness = %q, %v; wait=%v", line, err, waitErr)
			}
			if err := cmd.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			time.Sleep(25 * time.Millisecond)
			if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
				t.Fatalf("hard-ignored %s terminated helper: %v", name, err)
			}
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		})
	}
}
