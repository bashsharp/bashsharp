// Copyright (c) 2026, the outpost authors
// See LICENSE for licensing information

package interp

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

var childSignalStartMu sync.Mutex

var errReadInterrupted = errors.New("read interrupted by signal")

// isRuntimeSignal reports whether the named signal is a synchronous fault
// owned by the Go runtime. These signals cannot be safely reset, trapped, or
// ignored from user code because the Go runtime relies on its own handlers
// for panic / stack-overflow / fault recovery. On Linux the runtime installs
// sigaction handlers; on Darwin it complements Mach exception ports with
// signal handlers. Disturbing either would lose Go's fault-to-panic
// conversion and turn recoverable faults into process death.
func isRuntimeSignal(name string) bool {
	switch name {
	case "BUS", "FPE", "ILL", "SEGV", "TRAP":
		return true
	}
	return false
}

// A SignalResetter restores a signal's real OS default disposition
// (SIG_DFL) when the shell resets it with `trap - SIG`.
//
// This interpreter is an embeddable library: by default it never returns
// a signal to a process-terminating default disposition, because a shell
// script must not be able to kill the process that hosts it. A signal the
// script ignores with an empty-action trap is set to a real SIG_IGN (so exec'd
// children inherit it), but `trap - SIG` only tears down the shell's own
// Go-level handler — it does not re-arm the terminating default. That is
// safe for embedders, but it diverges from POSIX: a standalone shell that
// ignores USR2 and then runs `trap - USR2` must again be killed by a later
// SIGUSR2 under its default disposition.
//
// A host that presents itself as a standalone POSIX shell — such as the
// `bashy` CLI — closes that gap by opting in with [WithSignalResetter].
// The runner then calls ResetDefault whenever `trap - SIG` resets a real
// signal the shell had made non-default (ignored via an empty action or trapped
// via a handler), asking the host to install SIG_DFL for it so a
// subsequently-delivered signal takes its default action. num is the
// platform signal number and name is its canonical (no-"SIG") name.
//
// [OSSignalResetter] is the ready-made implementation the standalone host
// wires in; embedders that never opt in keep the safe default and are
// never terminated by a signal their script reset.
type SignalResetter interface {
	ResetDefault(num int, name string)
}

// StartupSignalIgnorer is an optional extension implemented by standalone
// signal hosts. When the process-boundary sideband says a signal was ignored
// on entry, the runner asks the host to restore SIG_IGN before any script can
// try to trap or reset it. Embedded hosts remain untouched unless they opt in.
type StartupSignalIgnorer interface {
	IgnoreStartup(num int, name string)
}

// WithSignalResetter makes `trap - SIG` restore a real OS default signal
// disposition through s. Only a host that owns the whole process — a
// standalone POSIX shell — should opt in; see [SignalResetter] for why the
// default is to leave process-global dispositions untouched. The bashy CLI
// wires this as interp.WithSignalResetter(interp.OSSignalResetter{}).
func WithSignalResetter(s SignalResetter) RunnerOption {
	return func(r *Runner) error {
		r.sigReset = s
		// The Go runtime installs handlers for several POSIX signals before
		// main. A standalone shell owns its process boundary and must expose
		// their kernel default actions: an external observer must see signal
		// termination (WIFSIGNALED), not a Go traceback plus exit status 2, and
		// must see terminal-stop signals enter WIFSTOPPED. Do not reset CHLD,
		// which drives child reaping, or URG, which the Go runtime uses for
		// asynchronous preemption. Embedded runners do not opt in here.
		if _, ok := s.(OSSignalResetter); ok {
			for _, name := range [...]string{
				"HUP", "INT", "QUIT", "ABRT",
				"USR1", "USR2", "PIPE", "ALRM", "TERM",
				"TSTP", "TTIN", "TTOU", "XCPU", "XFSZ",
			} {
				if sig, found := signalByName(name); found {
					// TP720: do not reset a signal that is currently SIG_IGN
					// at the OS level. It was inherited from the parent
					// process and POSIX requires it stay ignored. Checking
					// the real OS disposition (not signal.Ignored, which
					// only tracks signals set via os/signal.Ignore) is
					// necessary because this reset loop runs before Reset()
					// computes startupIgnored.
					if osSignalIgnored(signalForOS(sig)) {
						continue
					}
					// Keep os/signal's bookkeeping synchronized with the
					// standalone default reset, then install the real kernel
					// default. signal.Reset deliberately keeps handlingSig set
					// for signals whose handlers Go installs at startup, which
					// would make a later signal.Notify incorrectly believe the
					// handler was still active. Ignore clears that bookkeeping;
					// ResetDefault immediately replaces SIG_IGN with SIG_DFL.
					signal.Ignore(signalForOS(sig))
					s.ResetDefault(int(sigNum(sig)), name)
				}
			}
		}
		return nil
	}
}

// WithStandaloneSignalDefaults makes kill-delivered synchronous fault
// signals take their native default action when no shell trap or ignore owns
// them. The Go runtime otherwise turns externally delivered BUS/FPE/ILL/SEGV
// into a traceback and exit status 2, which is not a shell process's POSIX
// wait status. Genuine CPU faults remain owned by the Go runtime and still
// become Go panics; os/signal only redirects asynchronously delivered faults.
// Linux provides this distinction reliably. The option is currently a no-op
// on other platforms, where the runtime may classify an external fault as a
// synchronous one before os/signal can observe it.
//
// This option owns process-global signal state and is therefore only for a
// standalone shell process with one top-level Runner. Embedded users must not
// enable it. A standalone host should also provide [WithSignalResetter].
func WithStandaloneSignalDefaults() RunnerOption {
	return func(r *Runner) error {
		names := standaloneRuntimeDefaultSignalNames()
		if len(names) == 0 {
			return nil
		}
		r.sigMu.Lock()
		defer r.sigMu.Unlock()
		r.ensureSignalLoopLocked()
		for _, name := range names {
			if sig, ok := signalByName(name); ok {
				if r.standaloneDefaults == nil {
					r.standaloneDefaults = make(map[string]bool)
				}
				r.standaloneDefaults[name] = true
				r.startSignalSubscriptionLocked(name, signalForOS(sig), "", true)
			}
		}
		return nil
	}
}

// OSSignalResetter is the standalone-host [SignalResetter]: it installs the
// process's real SIG_DFL for the reset signal at the OS level.
//
// os/signal cannot express this. signal.Ignore sets SIG_IGN, but a later
// signal.Reset does not undo it (once Ignore clears the runtime's
// "handling" bit, Reset is a no-op and the disposition stays SIG_IGN), and
// even for a trapped signal Reset only reinstalls Go's own runtime handler,
// which silently swallows an un-notified _SigNotify signal like SIGUSR2
// rather than taking its default terminating action. ResetDefault instead
// reuses restoreExecSignal, which on Linux issues a raw rt_sigaction and on
// Darwin calls libc sigaction so its private kernel trampoline is supplied. A
// delivered SIGUSR2 then terminates the process as POSIX requires. Other
// platforms fall back to signal.Reset.
type OSSignalResetter struct{}

// ResetDefault implements [SignalResetter].
func (OSSignalResetter) ResetDefault(num int, name string) {
	sig, ok := signalByName(name)
	if !ok {
		return
	}
	restoreExecSignal(signalForOS(sig))
	busReset(signalForOS(sig))
}

// IsIgnored reports whether name currently has an ignored disposition,
// without changing it. Like ResetDefault, it resolves the canonical signal
// name; an unknown name returns false. Linux and Darwin query the kernel
// disposition directly. Other platforms use the existing os/signal fallback
// and cannot distinguish raw host changes from its bookkeeping.
func (OSSignalResetter) IsIgnored(num int, name string) bool {
	sig, ok := signalByName(name)
	return ok && osSignalIgnored(signalForOS(sig))
}

// IgnoreStartup implements [StartupSignalIgnorer].
func (OSSignalResetter) IgnoreStartup(num int, name string) {
	sig, ok := signalByName(name)
	if !ok {
		return
	}
	signal.Ignore(signalForOS(sig))
	busIgnore(signalForOS(sig))
}

func (r *Runner) restoreBridgedStartupIgnores() {
	ignorer, ok := r.sigReset.(StartupSignalIgnorer)
	if !ok {
		return
	}
	for name := range r.startupIgnored {
		sig, ok := signalByName(name)
		if !ok {
			continue
		}
		if isRuntimeSignal(name) {
			// A synchronous fault signal (BUS/FPE/ILL/SEGV/TRAP) that was
			// SIG_IGN on entry: the Go runtime overrode the inherited
			// SIG_IGN with its own fault handler before main, so a later
			// kill(2) (SI_USER) delivery reaches that handler and aborts
			// the process with a traceback instead of being ignored
			// (VSC-PCTS TP720). POSIX requires an ignored-on-entry signal
			// to stay ignored and untrappable. Reinstall a real SIG_IGN at
			// the OS level so a kill-delivered fault is discarded by the
			// kernel; a genuine hardware fault still terminates, matching
			// bash. Use the raw SIG_IGN (not signal.Ignore) so the
			// runtime's own bookkeeping is left untouched, exactly as
			// ignoreSignalTrap does for these signals.
			setOSIgnore(signalForOS(sig))
			continue
		}
		if num, ok := signalNumber(sig); ok {
			ignorer.IgnoreStartup(num, name)
		}
	}
}

type asyncExecSignal struct {
	name string
	sig  os.Signal
}

// signalDelivery carries the disposition that owned a signal when os/signal
// received it. Signal handlers run asynchronously, so consulting trapCallbacks
// later would incorrectly apply a newer trap (or a trap installed after an
// ignored/default delivery) to an older signal.
type signalDelivery struct {
	name          string
	callback      string
	sig           os.Signal
	defaultAction bool
}

type signalSubscription struct {
	ch            chan os.Signal
	done          chan struct{}
	finished      chan struct{}
	callback      string
	defaultAction bool
}

// monitorActive reports whether job-control monitor mode (`set -m` / `set -o
// monitor`) is currently in effect. A non-interactive shell defaults to off;
// the state is tracked in noOpSetState because the runner doesn't otherwise
// model job control.
func (r *Runner) monitorActive() bool {
	return r.noOpSetState["monitor"]
}

// guardUnmonitoredForegroundSignals keeps an interactive shell alive across
// terminal SIGINT/SIGQUIT delivered while it runs a foreground external command
// without job control. Real bash always keeps SIGINT caught while
// interactive — independent of monitor mode or any user `trap` — so it
// survives a terminal-delivered SIGINT even when `set +m` leaves the
// foreground child in the shell's own process group (no setpgid happens in
// prepareForegroundJobCmd/prepareBackgroundJobCmd once job control is off),
// which means the terminal signals target the shell too. With job control
// on, prepareForegroundJobCmd instead hands the terminal's controlling
// process group to the child, so the shell's own pgrp never receives the
// signal and this guard is unnecessary there.
//
// The returned func tears the guard down; it is always non-nil and safe to
// defer unconditionally, including when no guard was installed.
//
// A real trap or a hard/explicit ignore is already protective and takes
// priority independently for each signal, so this only fills gaps left when
// neither is set. Like bash's default interactive handlers, the guard runs no
// trap: it simply keeps the process off the terminating default dispositions.
func (r *Runner) guardUnmonitoredForegroundSignals(ctx context.Context) func() {
	noop := func() {}
	if r == nil || !r.interactiveShell || r.monitorActive() {
		return noop
	}
	if bg, _ := ctx.Value(bgProcCtxKey{}).(*bgProc); bg != nil {
		return noop
	}
	var guarded []os.Signal
	for _, name := range [...]string{"INT", "QUIT"} {
		if r.isStartupIgnored(name) || r.trapSignalActive(name) {
			continue
		}
		r.sigMu.Lock()
		ignored := r.sigIgnored[name]
		r.sigMu.Unlock()
		if ignored {
			continue
		}
		if sig, ok := signalByName(name); ok {
			guarded = append(guarded, signalForOS(sig))
		}
	}
	if len(guarded) == 0 {
		return noop
	}
	ch := make(chan os.Signal, 8)
	done := make(chan struct{})
	signal.Notify(ch, guarded...)
	go func() {
		for {
			select {
			case <-ch:
				// Discarded: matches bash's default interactive handling,
				// which runs no trap when none is set and only keeps the
				// process off the terminating default action.
			case <-done:
				return
			}
		}
	}()
	return func() {
		signal.Stop(ch)
		close(done)
	}
}

// ignoreAsyncListSignals applies POSIX 2.11 asynchronous-list signal
// defaults to a background runner when job control is disabled: SIGINT and
// SIGQUIT start ignored, but a trap command inside the async list may still
// replace or reset that disposition.
func (r *Runner) ignoreAsyncListSignals() {
	if r.monitorActive() {
		return
	}
	r.sigMu.Lock()
	defer r.sigMu.Unlock()
	if r.trapCallbacks == nil {
		r.trapCallbacks = make(map[string]string)
	}
	if r.asyncDefaultIgnored == nil {
		r.asyncDefaultIgnored = make(map[string]bool)
	}
	for _, sig := range [...]string{"INT", "QUIT"} {
		if _, ok := r.trapCallbacks[sig]; !ok {
			r.trapCallbacks[sig] = ""
			r.asyncDefaultIgnored[sig] = true
			delete(r.asyncDefaultReset, sig)
		}
	}
}

// setTrapCallback and removeTrapCallback are the only mutation paths for
// trapCallbacks once a runner is executing. The map is otherwise confined
// to the runner's own goroutine, but a background job with a carrier (see
// carrier.go) has a watcher goroutine classifying relayed signals via
// carrierSignalDisposition, which reads the map under sigMu — so every
// concurrent-time write must hold sigMu too. Same-goroutine reads (trap
// delivery, `trap -p`, subshell cloning) stay lock-free.
func (r *Runner) setTrapCallback(name, callback string) {
	r.sigMu.Lock()
	if r.trapCallbacks == nil {
		r.trapCallbacks = make(map[string]string)
	}
	delete(r.asyncDefaultIgnored, name)
	delete(r.asyncDefaultReset, name)
	r.trapCallbacks[name] = callback
	r.sigMu.Unlock()
}

func (r *Runner) removeTrapCallback(name string) {
	r.sigMu.Lock()
	if r.asyncDefaultIgnored[name] {
		if r.asyncDefaultReset == nil {
			r.asyncDefaultReset = make(map[string]bool)
		}
		r.asyncDefaultReset[name] = true
	}
	delete(r.asyncDefaultIgnored, name)
	delete(r.trapCallbacks, name)
	r.sigMu.Unlock()
}

func (r *Runner) asyncSignalExplicitlyReset(name string) bool {
	r.sigMu.Lock()
	defer r.sigMu.Unlock()
	return r.asyncDefaultReset[name]
}

func (r *Runner) asyncResetSignalsForExec(ctx context.Context) []asyncExecSignal {
	bg, _ := ctx.Value(bgProcCtxKey{}).(*bgProc)
	if bg == nil || bg.jobControl {
		return nil
	}
	var reset []asyncExecSignal
	for _, name := range [...]string{"INT", "QUIT"} {
		if !r.asyncDefaultReset[name] {
			continue
		}
		if sig, ok := signalByName(name); ok {
			reset = append(reset, asyncExecSignal{name: name, sig: signalForOS(sig)})
		}
	}
	return reset
}

func (r *Runner) asyncIgnoredSignalsForExec(ctx context.Context) []asyncExecSignal {
	bg, _ := ctx.Value(bgProcCtxKey{}).(*bgProc)
	if bg == nil || bg.jobControl {
		return nil
	}
	var ignored []asyncExecSignal
	for _, name := range [...]string{"INT", "QUIT"} {
		if cb, ok := r.trapCallbacks[name]; !ok || cb != "" {
			continue
		}
		if sig, ok := signalByName(name); ok {
			ignored = append(ignored, asyncExecSignal{name: name, sig: signalForOS(sig)})
		}
	}
	return ignored
}

// startExecCmd starts cmd after applying POSIX asynchronous-list signal
// inheritance. Go exposes no per-child pre-exec signal-disposition hook, so the
// runner serializes this narrow fork/exec window and temporarily sets SIG_IGN
// for the signals that `cmd &` defaults introduced.
func (r *Runner) startExecCmd(ctx context.Context, cmd *exec.Cmd) error {
	ignored := r.asyncIgnoredSignalsForExec(ctx)
	reset := r.asyncResetSignalsForExec(ctx)
	childSignalStartMu.Lock()
	defer childSignalStartMu.Unlock()
	ignoredSaved := make([]signalDisposition, len(ignored))
	ignoredSavedOK := make([]bool, len(ignored))
	for i, item := range ignored {
		// Preserve the exact process-global disposition around the narrow
		// fork/exec window. signal.Ignore tells os/exec that SIG_IGN must be
		// inherited rather than reset in the child; setOSIgnore makes that
		// kernel disposition explicit without relying on Go bookkeeping.
		ignoredSaved[i], ignoredSavedOK[i] = saveSignalDisposition(item.sig)
		signal.Ignore(item.sig)
		setOSIgnore(item.sig)
	}
	resetChans := make([]chan os.Signal, len(reset))
	resetSaved := make([]signalDisposition, len(reset))
	resetSavedOK := make([]bool, len(reset))
	for i, item := range reset {
		resetSaved[i], resetSavedOK[i] = saveSignalDisposition(item.sig)
		restoreExecSignal(item.sig)
		resetChans[i] = make(chan os.Signal, 1)
		signal.Notify(resetChans[i], item.sig)
	}
	err := cmd.Start()
	for i, item := range ignored {
		// Undo signal.Ignore's process-global Go bookkeeping before restoring
		// the exact raw disposition. Without this Reset, a later Notify guard
		// can leave the OS signal ignored even though sigaction says default.
		signal.Reset(item.sig)
		if ignoredSavedOK[i] {
			restoreSignalDisposition(item.sig, ignoredSaved[i])
		} else if r.startupIgnored[item.name] || r.sigIgnored[item.name] {
			// Unsupported platforms cannot snapshot raw sigaction bytes. Keep
			// the semantic fallback, including inherited and user ignores.
			setOSIgnore(item.sig)
		} else {
			restoreExecSignal(item.sig)
		}
	}
	for i, item := range reset {
		signal.Stop(resetChans[i])
		if resetSavedOK[i] {
			restoreSignalDisposition(item.sig, resetSaved[i])
		}
	}
	return err
}

// shellPid returns the PID this runner reports as $$, so the kill builtin can
// recognize a self-directed signal.
func (r *Runner) shellPid() int {
	if r.deterministic {
		return int(r.deterministicSeed & 0x7fff)
	}
	return os.Getpid()
}

// trapSignalActive reports whether this runner has installed an OS signal
// handler (via signal.Notify) for the named signal. A subshell clone returns
// false because the signal infrastructure is not inherited.
func (r *Runner) trapSignalActive(name string) bool {
	r.sigMu.Lock()
	defer r.sigMu.Unlock()
	_, ok := r.sigNotify[name]
	return ok
}

// pipelineSIGPIPEIgnored reports the inherited ignore disposition used by an
// in-process pipeline child. A caught trap is reset to default in that child,
// while an empty trap and a hard ignore remain ignored.
func (r *Runner) pipelineSIGPIPEIgnored() bool {
	r.sigMu.Lock()
	callback, set := r.trapCallbacks["PIPE"]
	r.sigMu.Unlock()
	return r.startupIgnored["PIPE"] || (set && callback == "")
}

// owningSignalRunner returns the runner whose installed OS handler owns the
// named signal: this runner if it owns it, otherwise the nearest foreground
// ancestor (a self-directed `kill -SIG $$` inside a foreground subshell targets
// the parent shell's PID, so the parent's trap is the one that fires). Returns
// nil when no runner in the chain has an active handler, in which case the
// signal must be sent via the OS to take its default disposition.
func (r *Runner) owningSignalRunner(name string) *Runner {
	for cur := r; cur != nil; cur = cur.sigParent {
		if cur.trapSignalActive(name) {
			return cur
		}
	}
	return nil
}

// parseHardIgnore decodes the comma-separated signal-name list carried in
// BashyHardIgnoreEnv into a set of canonical (no-SIG-prefix) names. Unknown or
// empty entries are dropped. Returns nil when there is nothing to ignore.
func parseHardIgnore(s string) map[string]bool {
	if s == "" {
		return nil
	}
	var set map[string]bool
	for _, name := range strings.Split(s, ",") {
		sig := normalizeSignal(name)
		if sig == "" {
			continue
		}
		if _, ok := signalByName(sig); !ok {
			continue // pseudo-signals can't be hard-ignored
		}
		if set == nil {
			set = make(map[string]bool)
		}
		set[sig] = true
	}
	return set
}

// hardIgnoreEnvValue serialises the signals this runner ignores for the
// BashyHardIgnoreEnv bridge, so a child shell we exec treats them as
// ignored-on-entry. Returns "" when none are ignored.
//
// Both kinds of ignore go across, because execve does not distinguish
// them: a signal this shell set to SIG_IGN with `trap ” SIG` and one that
// was already SIG_IGN when the shell started (its own startupIgnored, from
// a parent that did the same) are both SIG_IGN in the process the exec
// replaces, and both are inherited. Carrying only the first would lose an
// inherited ignore at the second hop of a chain of execs.
func (r *Runner) hardIgnoreEnvValue() string {
	r.sigMu.Lock()
	defer r.sigMu.Unlock()
	if len(r.sigIgnored) == 0 && len(r.startupIgnored) == 0 {
		return ""
	}
	seen := make(map[string]bool, len(r.sigIgnored)+len(r.startupIgnored))
	names := make([]string, 0, len(r.sigIgnored)+len(r.startupIgnored))
	for _, set := range []map[string]bool{r.sigIgnored, r.startupIgnored} {
		for name := range set {
			if seen[name] {
				continue
			}
			seen[name] = true
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}

// isStartupIgnored reports whether the named real signal was SIG_IGN when this
// process started. Bash flags such signals as SIG_HARD_IGNORE: a `trap` that
// tries to set or reset them is a silent no-op, and they are listed with an
// empty action. Pseudo-signals are never startup ignored.
func (r *Runner) isStartupIgnored(name string) bool {
	if _, ok := signalByName(name); !ok {
		return false
	}
	return r.startupIgnored[name]
}

// notifyChildReaped queues one SIGCHLD trap run for a child that has just been
// reaped, matching bash's run_sigchld_trap-per-reaped-child in waitchld
// (jobs.c): each reaped child runs the trap once, fired at the next statement
// boundary. A no-op when no SIGCHLD trap is active. Driving CHLD from reaps
// rather than from the OS signal is what makes it deterministic — the kernel
// coalesces simultaneous child deaths into a single SIGCHLD, losing traps.
func (r *Runner) notifyChildReaped() {
	if !r.chldTrapActive.Load() {
		return
	}
	r.markPendingSignal("CHLD")
}

// enableSignalTrap ensures the OS signal named is delivered to this runner's
// pending-signal queue. Called by the `trap` builtin when a non-empty handler
// is registered for a real signal. Pseudo-signals (EXIT/ERR/DEBUG/RETURN) are
// not OS signals and are ignored here.
func (r *Runner) enableSignalTrap(name string) {
	// OS signal dispositions are process-global, whereas a Bash++ task is an
	// in-process structured child. Keep the callback in the task's private map
	// but never let a task install a process-global signal subscription.
	if r.inBashPPTask() {
		return
	}
	if r.isStartupIgnored(name) {
		return
	}
	// KILL and STOP are recorded in trapCallbacks for bash-compatible
	// reporting, but the kernel never permits a handler for either signal.
	// In particular, do not strand a forwarder on a signal.Notify channel
	// which can never receive a delivery.
	if name == "KILL" || name == "STOP" {
		return
	}
	// SIGCHLD is driven from child reaps (notifyChildReaped), not from OS
	// signal delivery, so it never needs an OS handler. Installing one would
	// also risk perturbing Go's own child reaping.
	if name == "CHLD" {
		return
	}
	// SIGURG is used by the Go runtime for goroutine async-preemption. A
	// signal.Notify handler for it catches the runtime's OWN internal preemption
	// signals — they cannot be distinguished from an external SIGURG once
	// notified (a documented Go limitation) — which would fire the shell's trap
	// thousands of times per second and corrupt any harness that traps signal 23
	// (e.g. the VSC-PCTS TCM). A pure-Go shell therefore cannot deliver SIGURG
	// traps: record the trap (so `trap -p` shows it) but install no OS handler.
	if name == "URG" {
		return
	}
	sig, ok := signalByName(name)
	if !ok {
		return
	}
	r.sigMu.Lock()
	oldSub := r.stopSignalSubscriptionLocked(name)
	if r.sigIgnored[name] {
		// TP714: record that this signal was ignored so the Notify block
		// below knows to restoreExecSignal before signal.Notify.
		if r.sigIgnoredPreReset == nil {
			r.sigIgnoredPreReset = make(map[string]bool)
		}
		r.sigIgnoredPreReset[name] = true
	}
	delete(r.sigIgnored, name)
	r.ensureSignalLoopLocked()
	osSig := signalForOS(sig)
	if disposition, ok := r.sigIgnoredRestore[name]; ok {
		restoreSignalDisposition(osSig, disposition)
		delete(r.sigIgnoredRestore, name)
		delete(r.sigIgnoredPreReset, name)
	}
	if r.sigIgnoredPreReset[name] {
		// TP714: signal.Ignore clears Go's internal handling bit,
		// after which signal.Notify cannot re-enable delivery on
		// some platforms. Call restoreExecSignal to reinstall
		// SIG_DFL via raw sigaction before Notify.
		restoreExecSignal(osSig)
		delete(r.sigIgnoredPreReset, name)
	}
	// Synchronize the Go runtime before installing a new trap. This is
	// needed after both the standalone startup reset and trap '' SIG.
	signal.Reset(osSig)
	busReset(osSig)
	r.sigNotify[name] = osSig
	r.startSignalSubscriptionLocked(name, osSig, r.trapCallbacks[name], false)
	r.sigMu.Unlock()
	r.waitSignalSubscription(oldSub)
}

func (r *Runner) ensureSignalLoopLocked() {
	if r.sigNotify != nil {
		return
	}
	r.sigNotify = make(map[string]os.Signal)
	r.pendingSig = make(map[string]int)
	r.pendingSigCallback = make(map[string][]string)
	r.sigWake = make(chan struct{}, 1)
}

// startSignalSubscriptionLocked gives each OS subscription its own channel
// and snapshots the disposition that owns deliveries from that channel. This
// keeps a queued signal from being interpreted using a later trap setting.
func (r *Runner) startSignalSubscriptionLocked(name string, sig os.Signal, callback string, defaultAction bool) {
	ch := make(chan os.Signal, 1)
	done := make(chan struct{})
	finished := make(chan struct{})
	if r.sigNotifyCh == nil {
		r.sigNotifyCh = make(map[string]signalSubscription)
	}
	sub := signalSubscription{ch: ch, done: done, finished: finished, callback: callback, defaultAction: defaultAction}
	r.sigNotifyCh[name] = sub
	signal.Notify(ch, sig)
	busNotify(ch, sig)
	go r.forwardSignalSubscription(name, sub)
}

// stopSignalSubscriptionLocked stops future notifications and asks the
// subscription worker to drain anything already queued with its original
// disposition. It only changes subscription state; callers must wait for the
// returned worker after releasing sigMu, since a draining worker enqueues into
// the same mutex.
func (r *Runner) stopSignalSubscriptionLocked(name string) *signalSubscription {
	sub, ok := r.sigNotifyCh[name]
	if !ok {
		return nil
	}
	signal.Stop(sub.ch)
	busStop(sub.ch)
	close(sub.done)
	delete(r.sigNotifyCh, name)
	return &sub
}

func (r *Runner) waitSignalSubscription(sub *signalSubscription) {
	if sub != nil {
		<-sub.finished
	}
}

// stopSignalSubscriptions stops and joins every subscription currently owned
// by r. Reset replaces the Runner value wholesale, so it must release these
// workers before their lifetime map is discarded. Waiting happens after
// sigMu is released because a worker draining an accepted delivery needs that
// mutex to enqueue it.
func (r *Runner) stopSignalSubscriptions() {
	r.sigMu.Lock()
	subs := make([]signalSubscription, 0, len(r.sigNotifyCh))
	for name := range r.sigNotifyCh {
		if sub := r.stopSignalSubscriptionLocked(name); sub != nil {
			subs = append(subs, *sub)
		}
	}
	r.sigMu.Unlock()
	for i := range subs {
		r.waitSignalSubscription(&subs[i])
	}
}

// forwardSignalSubscription never holds sigMu while waiting for a channel.
// Once stopped, signal.Stop guarantees no new OS delivery and the worker drains
// its buffered channel before declaring itself finished, retaining the callback
// snapshot attached to each delivery.
func (r *Runner) forwardSignalSubscription(name string, sub signalSubscription) {
	defer close(sub.finished)
	for {
		select {
		case sig := <-sub.ch:
			r.handleSignalDelivery(signalDelivery{name: name, callback: sub.callback, sig: sig, defaultAction: sub.defaultAction})
		case <-sub.done:
			for {
				select {
				case sig := <-sub.ch:
					r.handleSignalDelivery(signalDelivery{name: name, callback: sub.callback, sig: sig, defaultAction: sub.defaultAction})
				default:
					return
				}
			}
		}
	}
}

// ignoreSignalTrap sets a real OS SIG_IGN for the named signal. Unlike a
// notified trap (which installs a Go handler and is therefore reset to SIG_DFL
// across execve), a real SIG_IGN is inherited as SIG_IGN by exec'd children,
// which is exactly how bash makes an ignored signal hard-ignored in a child
// shell (trap.tests/trap1.sub). Pseudo-signals are ignored here; the empty
// trapCallbacks entry alone records their state.
func (r *Runner) ignoreSignalTrap(name string) {
	if r.inBashPPTask() {
		return
	}
	if r.isStartupIgnored(name) {
		return
	}
	// Preserve the trap metadata while leaving the impossible OS disposition
	// untouched for signals which cannot be caught or ignored.
	if name == "KILL" || name == "STOP" {
		return
	}
	// SIGCHLD is reap-driven; never touch its OS disposition. A real SIG_IGN
	// on SIGCHLD would also break child reaping (auto-reaped children make
	// wait() fail with ECHILD on some systems).
	if name == "CHLD" {
		return
	}
	sig, ok := signalByName(name)
	if !ok {
		return
	}
	r.sigMu.Lock()
	oldSub := r.stopSignalSubscriptionLocked(name)
	delete(r.sigNotify, name)
	if r.sigIgnored == nil {
		r.sigIgnored = make(map[string]bool)
	}
	r.sigIgnored[name] = true
	osSig := signalForOS(sig)
	busIgnore(osSig)
	if isRuntimeSignal(name) {
		// Do not call signal.Ignore for a Go runtime-owned fault signal.
		// signal.Ignore clears the runtime's internal delivery bit; a later
		// signal.Notify then cannot reliably restore delivery after
		// `trap '' BUS; trap action BUS` (VSC TP714). A raw SIG_IGN gives
		// exec'd children the required inherited disposition while retaining
		// the runtime handler needed when the shell starts trapping again.
		if disposition, ok := saveSignalDisposition(osSig); ok {
			if r.sigIgnoredRestore == nil {
				r.sigIgnoredRestore = make(map[string]signalDisposition)
			}
			r.sigIgnoredRestore[name] = disposition
		}
		setOSIgnore(osSig)
		r.sigMu.Unlock()
		r.waitSignalSubscription(oldSub)
		return
	}
	signal.Ignore(osSig) // also undoes any prior signal.Notify for sig
	r.sigMu.Unlock()
	r.waitSignalSubscription(oldSub)
}

// disableSignalTrap stops OS delivery for the named signal, restoring its
// default disposition. Called by the `trap` builtin when a trap is reset.
func (r *Runner) disableSignalTrap(name string) {
	if r.inBashPPTask() {
		return
	}
	if r.isStartupIgnored(name) {
		return
	}
	if name == "KILL" || name == "STOP" {
		return
	}
	sig, ok := signalByName(name)
	if !ok {
		return
	}
	r.sigMu.Lock()
	_, hadNotify := r.sigNotify[name]
	_, hadIgnore := r.sigIgnored[name]
	oldSub := r.stopSignalSubscriptionLocked(name)
	if hadNotify || hadIgnore {
		delete(r.sigNotify, name)
		delete(r.sigIgnored, name)
		signal.Reset(signalForOS(sig))
		busReset(signalForOS(sig))
	}
	sigReset := r.sigReset
	standaloneDefault := r.standaloneDefaults[name]
	r.sigMu.Unlock()
	// signal.Reset above tears down the shell's Go-level handler but does
	// not, on its own, re-arm a terminating SIG_DFL after a `trap ''`
	// SIG_IGN. A standalone host opts in via WithSignalResetter to restore
	// the real OS default disposition, so a signal delivered after
	// `trap - SIG` again takes its default action (POSIX). Embedded hosts
	// leave sigReset nil and are never terminated by a reset they didn't
	// ask to re-arm. Run outside sigMu: the host callback touches process
	// signal state, not this runner's maps.
	if sigReset != nil && (hadNotify || hadIgnore) {
		if num, ok := signalNumber(sig); ok {
			sigReset.ResetDefault(num, name)
		}
	}
	if standaloneDefault {
		r.sigMu.Lock()
		r.ensureSignalLoopLocked()
		r.startSignalSubscriptionLocked(name, signalForOS(sig), "", true)
		r.sigMu.Unlock()
	}
	r.waitSignalSubscription(oldSub)
}

// handleSignalDelivery records a receipt-time delivery and wakes any blocked
// wait. A standalone runner relays a fault signal that arrived under its
// native-default subscription; a later trap must not turn that delivery into a
// trap action.
func (r *Runner) handleSignalDelivery(delivery signalDelivery) {
	if delivery.defaultAction {
		// os/signal sends only asynchronously generated instances here; a
		// genuine CPU fault remains on the runtime's synchronous panic path.
		relayRuntimeDefaultSignal(delivery.sig)
		return
	}
	r.queuePendingSignal(delivery.name, delivery.callback)
}

// markPendingSignal queues a synchronously-delivered signal (a self-directed
// `kill` whose trap this runner owns), so the next statement boundary runs the
// handler without relying on OS signal delivery, which would race.
func (r *Runner) markPendingSignal(name string) {
	r.sigMu.Lock()
	callback := r.trapCallbacks[name]
	r.sigMu.Unlock()
	r.queuePendingSignal(name, callback)

}

func (r *Runner) queuePendingSignal(name, callback string) {
	r.sigMu.Lock()
	if r.pendingSig == nil {
		r.pendingSig = make(map[string]int)
	}
	if r.pendingSigCallback == nil {
		r.pendingSigCallback = make(map[string][]string)
	}
	r.pendingSig[name]++
	r.pendingSigCallback[name] = append(r.pendingSigCallback[name], callback)
	// Publish the fast-path bit before a receipt wakes a foreground waiter.
	// Otherwise that waiter can return and miss the trap at its next statement
	// boundary while this goroutine has not yet stored the bit.
	r.hasPendingSig.Store(true)
	if receipt := r.groupSignalReceipt[name]; receipt != nil {
		close(receipt)
		delete(r.groupSignalReceipt, name)
	}
	r.sigMu.Unlock()
	r.wakeSignalWaiters()
}

func (r *Runner) wakeSignalWaiters() {
	r.sigMu.Lock()
	wake := r.sigWake
	r.sigMu.Unlock()
	if wake == nil {
		return
	}
	select {
	case wake <- struct{}{}:
	default:
	}
}

func (r *Runner) signalWakeChan() <-chan struct{} {
	r.sigMu.Lock()
	defer r.sigMu.Unlock()
	return r.sigWake
}

// nextPendingSignal pops the lowest-numbered pending signal and its
// receipt-time callback, or ("", "") if none remain.
func (r *Runner) nextPendingSignal() (string, string) {
	r.sigMu.Lock()
	defer r.sigMu.Unlock()
	best := ""
	bestNum := 1 << 30
	for n, c := range r.pendingSig {
		if c <= 0 {
			continue
		}
		num := 1 << 29
		if s, ok := signalByName(n); ok {
			if n, ok := signalNumber(s); ok {
				num = n
			}
		}
		if num < bestNum {
			bestNum = num
			best = n
		}
	}
	if best != "" {
		r.pendingSig[best]--
		callbacks := r.pendingSigCallback[best]
		if len(callbacks) > 0 {
			callback := callbacks[0]
			r.pendingSigCallback[best] = callbacks[1:]
			return r.finishPendingSignalLocked(best), callback
		}
	}
	return r.finishPendingSignalLocked(best), ""
}

func (r *Runner) finishPendingSignalLocked(best string) string {
	any := false
	for _, c := range r.pendingSig {
		if c > 0 {
			any = true
			break
		}
	}
	if !any {
		r.hasPendingSig.Store(false)
	}
	return best
}

// peekPendingSignal returns the lowest-numbered pending signal's name and
// number without consuming it, or ("", 0) if none are pending.
func (r *Runner) peekPendingSignal() (string, int) {
	r.sigMu.Lock()
	defer r.sigMu.Unlock()
	best := ""
	bestNum := 1 << 30
	for n, c := range r.pendingSig {
		if c <= 0 {
			continue
		}
		num := 1 << 29
		if s, ok := signalByName(n); ok {
			if n, ok := signalNumber(s); ok {
				num = n
			}
		}
		if num < bestNum {
			bestNum = num
			best = n
		}
	}
	if best == "" {
		return "", 0
	}
	return best, bestNum
}

// groupSignalReceiptChan subscribes to this runner's next OS signal receipt
// before kill(2) sends to a group containing this shell. Closing the channel
// broadcasts receipt to every waiter; the separate sigWake channel belongs
// to wait(1) and must not be consumed here. An already-pending trap is enough
// to run before the next statement, and a second standard signal may coalesce
// with it, so there is no new receipt to wait for in that case.
func (r *Runner) groupSignalReceiptChan(name string) <-chan struct{} {
	r.sigMu.Lock()
	defer r.sigMu.Unlock()
	if r.pendingSig[name] > 0 {
		return nil
	}
	if r.groupSignalReceipt == nil {
		r.groupSignalReceipt = make(map[string]chan struct{})
	}
	if r.groupSignalReceipt[name] == nil {
		r.groupSignalReceipt[name] = make(chan struct{})
	}
	return r.groupSignalReceipt[name]
}

// waitForForegroundGroupSignalReceipt closes the race between reaping a
// signaled foreground command and receiving the same process-group signal in
// this shell. If the command was the file's last statement, returning from
// its wait before our os/signal forwarder runs would discard the shell's trap.
// A signal sent only to the command has no shell receipt, so bound the join.
// Ordinary foreground completions never enter this path; a signal-only child
// death with an active matching trap can add at most this one bound.
func (r *Runner) waitForForegroundGroupSignalReceipt(ctx context.Context, name string) {
	r.sigMu.Lock()
	_, trapped := r.trapCallbacks[name]
	_, subscribed := r.sigNotifyCh[name]
	r.sigMu.Unlock()
	if !trapped || !subscribed {
		return
	}
	receipt := r.groupSignalReceiptChan(name)
	if receipt == nil { // already pending
		return
	}
	const receiptBound = 200 * time.Millisecond
	timer := time.NewTimer(receiptBound)
	defer timer.Stop()
	select {
	case <-receipt:
	case <-timer.C:
	case <-ctx.Done():
	}
	// A command-only signal does not leave an unused receipt subscription
	// that could be mistaken for a later process-group delivery.
	r.sigMu.Lock()
	if r.groupSignalReceipt[name] == receipt {
		delete(r.groupSignalReceipt, name)
	}
	r.sigMu.Unlock()
}

// waitOrSignal blocks until bg finishes or a trapped signal arrives, returning
// true if a signal interrupted the wait. POSIX requires `wait` to return
// immediately (with status >128) when a trapped signal is received; the trap
// itself runs at the next statement boundary via deliverPendingSignals.
func (r *Runner) waitOrSignal(ctx context.Context, bg *bgProc) (interrupted bool) {
	r.sigMu.Lock()
	wake := r.sigWake
	r.sigMu.Unlock()
	for {
		select {
		case <-ctx.Done():
			return true
		case <-bg.done:
			return false
		case <-wake:
			// SIGCHLD does not interrupt wait — bash runs its trap per
			// child death but keeps waiting. The pending CHLD trap fires
			// at the next statement boundary. Any other trapped signal
			// returns immediately (POSIX).
			if name, _ := r.peekPendingSignal(); name != "" && name != "CHLD" {
				return true
			}
			// CHLD or spurious wake; keep waiting
		}
	}
}

// deliverPendingSignals runs the trap handlers for any signals that have
// arrived since the last check. It is called at statement boundaries. The
// handlers' control flow (return/exit/break/continue) is allowed to propagate
// into the surrounding execution, matching bash's behaviour where a trap fired
// mid-function can unwind it.
func (r *Runner) deliverPendingSignals(ctx context.Context) {
	if r.handlingTrap || !r.hasPendingSig.Load() {
		return
	}
	for {
		name, cb := r.nextPendingSignal()
		if name == "" {
			return
		}
		r.runSignalTrap(ctx, cb, name)
		if r.exit.returning || r.exit.exiting || r.exit.fatalExit ||
			r.breakEnclosing > 0 || r.contnEnclosing > 0 {
			return
		}
	}
}

// runSignalTrap parses and runs a signal trap handler. Unlike
// [Runner.trapCallback] (used for EXIT/ERR/DEBUG/RETURN), it lets the handler's
// control flow propagate: a `return`, `exit`, `break`, or `continue` in the
// handler takes effect in the interrupted code. The pre-trap exit status is
// preserved when the handler completes without altering control flow.
func (r *Runner) runSignalTrap(ctx context.Context, callback, name string) {
	if callback == "" {
		return // explicitly-ignored signal (`trap '' SIG`)
	}
	savedAgentic := r.bashPPAgentic
	r.bashPPAgentic = false
	defer func() { r.bashPPAgentic = savedAgentic }()
	wasHandling := r.handlingTrap
	r.handlingTrap = true
	defer func() { r.handlingTrap = wasHandling }()

	if s, ok := signalByName(name); ok {
		if n, ok := signalNumber(s); ok {
			r.setVarString("BASH_TRAPSIG", strconv.Itoa(n))
		}
	}

	p := syntax.NewParser()
	if r.Dialect() == syntax.LangBashPP {
		syntax.Variant(syntax.LangBashPP)(p)
	}
	file, err := p.Parse(strings.NewReader(callback), name+" trap")
	if err != nil {
		return // ignore parse errors in the callback, as trapCallback does
	}
	oldExit := r.exit
	// POSIX interp 1602: a bare `return` directly in this action returns
	// the $? in effect at trap entry. Record it and the call depth so the
	// return builtin can tell action-level from nested-function returns.
	oldInSig, oldDepth, oldSaved := r.inSignalTrap, r.signalTrapDepth, r.signalTrapExit
	r.inSignalTrap = true
	r.signalTrapDepth = len(r.callStack)
	r.signalTrapExit = oldExit.code
	defer func() {
		r.inSignalTrap, r.signalTrapDepth, r.signalTrapExit = oldInSig, oldDepth, oldSaved
	}()
	r.exit = exitStatus{code: oldExit.code} // the handler sees the prior $?
	// The action is re-parsed and re-run on every delivery, so it must see
	// the alias definitions in effect now (when the trap fires), not be
	// suppressed by alias-timing against the freshly-parsed body's line 1.
	r.withAliasReparse(r.trapAliasReparseLine(), func() {
		r.stmts(ctx, file.Stmts)
	})
	if r.exit.returning || r.exit.exiting || r.exit.fatalExit ||
		r.breakEnclosing > 0 || r.contnEnclosing > 0 {
		return // let the handler's control flow unwind the interrupted code
	}
	r.exit = oldExit
}
