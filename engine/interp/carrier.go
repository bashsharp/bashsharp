// Copyright (c) 2026, the bashy authors
// See LICENSE for licensing information

package interp

import (
	"context"
	"fmt"
	"sort"
)

// A JobCarrier supplies kernel-visible stand-in processes ("carriers")
// for background jobs. This interpreter runs asynchronous lists
// (`cmd &`) as goroutines rather than forked shells, so a job made only
// of builtins or compound commands has no OS process of its own and
// `$!` falls back to the opaque "g<N>" handle. A host that needs strict
// POSIX process semantics — every `$!` a real, signalable kernel PID —
// opts in with [WithJobCarrier]; see that option for the semantics.
//
// Implementations should launch a process that blocks until killed or
// told to exit. Tying the carrier's stdin to a pipe held by the host is
// a good pattern: the carrier exits on EOF, so it cannot outlive a host
// that dies without cleaning up. Hosts that enable job control
// (`set -m`) should also place each carrier in its own process group,
// because group-directed signals target the negated PID.
type JobCarrier interface {
	// StartCarrier launches one carrier process. ctx is the context the
	// background job will run under; implementations may use it during
	// startup but should not tie the carrier's lifetime to it — the
	// runner reaps carriers explicitly via [CarrierProcess.Terminate].
	StartCarrier(ctx context.Context) (CarrierProcess, error)
}

// IgnoredSignalJobCarrier is the optional carrier extension for hosts whose
// helper process must explicitly reconstruct ignored dispositions after exec.
// ignored contains canonical signal names without the "SIG" prefix, captured
// from the background runner before it starts. The slice belongs to the
// callee. Hosts which inherit SIG_IGN naturally can implement JobCarrier only.
type IgnoredSignalJobCarrier interface {
	JobCarrier
	StartCarrierWithIgnoredSignals(ctx context.Context, ignored []string) (CarrierProcess, error)
}

// CarrierProcess is one live carrier obtained from [JobCarrier.StartCarrier].
type CarrierProcess interface {
	// Pid returns the carrier's kernel PID, which must be positive.
	Pid() int

	// Wait blocks until the carrier process has exited and been reaped,
	// returning the number of the signal that terminated it, or 0 if it
	// exited normally. The runner calls it exactly once, from a
	// goroutine it owns.
	Wait() int

	// Terminate makes the carrier exit promptly. It must be idempotent
	// and safe to call concurrently with Wait, including after the
	// carrier has already died.
	Terminate()
}

// ProcessGroupCarrierProcess is the optional job-control extension for a
// carrier which is the stable leader of its own operating-system process
// group. ProcessGroupID must remain positive and usable until Terminate is
// called. When monitor mode is active, the runner places every external child
// of the represented job in this group, so pipelines, jobs -p, and job-spec
// signals share one kernel identity even when a short-lived pipeline child
// exits while its siblings are still starting.
type ProcessGroupCarrierProcess interface {
	CarrierProcess
	ProcessGroupID() int
	// ResumeProcessGroupLeader continues the carrier proxy itself after a
	// group-directed job-control stop. It must not continue the rest of the
	// process group; bg/fg or an explicit signal owns that transition.
	ResumeProcessGroupLeader() error
}

// CarrierWaitState is one observable carrier process state. A terminal state
// means the process has exited and been reaped. A stopped state is
// non-terminal: WaitState must be called again after the process is continued
// or terminated to perform the final reap.
type CarrierWaitState struct {
	Signal  int
	Stopped bool
}

// StopAwareCarrierProcess is the optional extension for hosts which can
// observe child stops (for example with waitpid(WUNTRACED) on Unix). The
// ordinary os/exec Cmd.Wait only returns for terminal states, so it cannot
// relay SIGSTOP/SIGTSTP delivered to a carrier while the represented shell job
// is still running. Hosts implementing this extension let the runner react to
// a stop promptly, then terminate and reap the carrier.
//
// WaitState has the same single-waiter rule as CarrierProcess.Wait. The runner
// uses one API or the other, never both.
type StopAwareCarrierProcess interface {
	CarrierProcess
	WaitState() CarrierWaitState
}

// CarrierSignalState optionally reports a catchable signal received by a live
// proxy. Delivered states do not imply that the carrier stopped or exited.
type CarrierSignalState struct {
	CarrierWaitState
	Delivered bool
}

// SignalAwareCarrierProcess preserves a carrier across ignored and trapped
// signals. The runner consumes either WaitSignalState, WaitState, or Wait with
// one waiter, never multiple APIs. Terminal states must follow every already
// received signal event and join any implementation-owned readers.
type SignalAwareCarrierProcess interface {
	StopAwareCarrierProcess
	WaitSignalState() CarrierSignalState
}

// WithJobCarrier gives background jobs a kernel-visible identity. For
// each asynchronous list (`cmd &`, including `a | b &`) the runner
// starts one carrier process via c and uses its real PID instead of the
// synthetic "g<N>" handle. That identity is stable for the job's lifetime:
// external children remain implementation details, and signals sent to `$!`
// enter through the carrier's relay and wait-status machinery.
//
// The carrier is a signal proxy, not the job itself. A signal that
// terminates the carrier is relayed to the job according to the job's
// own disposition for that signal at that moment, like a signal
// arriving at a real shell child:
//
//   - Trapped (a non-empty `trap` action, set inside the async list or
//     inherited from the parent shell): the signal is queued into the
//     job's pending-signal machinery and the trap action runs at the
//     job's next statement boundary. The action decides the job's fate —
//     it may `exit`, or return and leave the job running. This works
//     even for a signal relayed before the job has started running.
//   - Ignored (`trap ” SIG`, a signal hard-ignored at shell startup, or
//     SIGINT/SIGQUIT's default ignore in an asynchronous list without
//     job control): the job keeps running, unaffected.
//   - Default — and always for the uncatchable SIGKILL and SIGSTOP —
//     the signal terminates the job: the runner cancels the job's
//     context (killing any external child it is currently running) and
//     the job's exit status becomes 128+signal (e.g. 143 for SIGTERM,
//     137 for SIGKILL), matching a real shell child.
//
// A basic JobCarrier keeps its default dispositions: external `kill` decides
// the job's fate by killing the carrier, and the relay above happens when the
// runner reaps it. An IgnoredSignalJobCarrier may instead preserve the
// dispositions ignored when the background runner starts, keeping carrier
// identity for those signals. A disposition changed later by the job can
// still outlive a dead carrier; `wait` and `jobs` continue to resolve it.
// When the job finishes first, the runner reaps the carrier via
// [CarrierProcess.Terminate] and waits for its [CarrierProcess.Wait] to
// return before sealing the exit status, so a racing external kill
// either lands fully or is cleanly ignored, and by the time `wait`
// unblocks the carrier PID is truly gone — a following `kill -0 $!` sees
// no such process rather than a lingering carrier. PIDs of external
// processes the job spawns are still recorded, so `wait`/`kill` on those
// also resolve to the job.
//
// Opting in is a strict contract: the runner never falls back to a
// synthetic identity. If StartCarrier fails, or hands back a carrier
// with a nonpositive PID, the job is not started at all — the runner
// prints a diagnostic to stderr and the asynchronous statement fails
// with exit status 1, like a shell whose fork failed; `$!` keeps its
// previous value. Embedders that do not opt in keep the opaque "g<N>"
// handles and cannot claim strict process semantics for `$!`. Coprocs
// keep their existing synthetic `<NAME>_PID` and process substitutions
// are not jobs; neither uses a carrier. The option is ignored under
// `set -o dryrun` and in deterministic mode, which must not observe
// real PIDs.
//
// With a carrier configured, [WithBgPidCallback] fires once per
// background job with the carrier PID, rather than once per external
// process the job spawns.
func WithJobCarrier(c JobCarrier) RunnerOption {
	return func(r *Runner) error {
		r.jobCarrier = c
		return nil
	}
}

// attachCarrier gives bg a kernel-visible identity via the runner's
// JobCarrier, if any. Called from the parent goroutine after bg.cancel
// is set and before the job goroutine starts, so the identity is fixed
// from birth and `$!` never blocks on pidReady. job is the runner that
// will execute the asynchronous list; carrier signals are relayed into
// its trap machinery. A nil return with no carrier configured (or in
// dryrun/deterministic mode) keeps the legacy g<N> handle; a non-nil
// error means the job must not run at all — the host opted into real
// kernel identities, so there is no identity to degrade to.
func (r *Runner) attachCarrier(ctx context.Context, job *Runner, bg *bgProc) error {
	if r.jobCarrier == nil || r.dryRun || r.deterministic {
		return nil
	}
	var cp CarrierProcess
	var err error
	if aware, ok := r.jobCarrier.(IgnoredSignalJobCarrier); ok {
		cp, err = aware.StartCarrierWithIgnoredSignals(ctx, job.carrierIgnoredSignalNames())
	} else {
		cp, err = r.jobCarrier.StartCarrier(ctx)
	}
	if err != nil {
		if cp != nil {
			go func() {
				cp.Terminate()
				cp.Wait()
			}()
		}
		return fmt.Errorf("job carrier: %v", err)
	}
	if cp == nil {
		return fmt.Errorf("job carrier: no carrier process")
	}
	pid := cp.Pid()
	if pid <= 0 {
		// A carrier without a usable PID is of no use; reap it and
		// report the failure.
		go func() {
			cp.Terminate()
			cp.Wait()
		}()
		return fmt.Errorf("job carrier: invalid carrier pid %d", pid)
	}
	if bg.jobControl {
		if grouped, ok := cp.(ProcessGroupCarrierProcess); ok {
			pgrp := grouped.ProcessGroupID()
			if pgrp <= 0 {
				go func() {
					cp.Terminate()
					cp.Wait()
				}()
				return fmt.Errorf("job carrier: invalid process group %d", pgrp)
			}
			bg.pgrp.Store(int64(pgrp))
			bg.pgrpFixed = true
		}
	}
	bg.carrier = cp
	bg.carrierSignalRunner.Store(job)
	bg.carrierDone = make(chan struct{})
	// The carrier PID seeds the job identity (bg.pid) as a stand-in so a
	// compound job that never execs (a brace group or builtin-only list)
	// still has a real PID for `$!` immediately. For a simple-call or
	// pipeline background job the caller leaves publishPidToBang true, so
	// pidReady stays open below until publishBgPid overrides bg.pid with
	// the real exec'd command PID — POSIX requires `$!` to be that PID, not
	// the carrier's (VSC-PCTS TP306/307/461/462). Compound commands keep
	// the carrier PID as their identity (analogous to bash's forked
	// subshell). `wait $!`/`kill $!` resolve either PID via
	// bgProc.matchesPid, which scans bg.pids.
	bg.pid.Store(int64(pid))
	// The carrier PID is also an entry in bg.pids so that wait/kill can
	// resolve the job by the carrier PID even after publishBgPid overwrites
	// bg.pid with the exec'd command's PID.
	bg.pidsMu.Lock()
	bg.pids = append(bg.pids, int64(pid))
	bg.pidsMu.Unlock()
	// Compound jobs use the carrier itself as $! and can publish it now.
	// Simple commands and pipelines must wait for their primary exec to
	// publish its PID; exposing the carrier in that window makes an immediate
	// `pid=$!` disagree with the PID observed as $$ by the executed command.
	if !bg.publishPidToBang {
		close(bg.pidReady)
	}
	if bg.pidCallback != nil {
		bg.pidCallback(pid)
	}
	go func() {
		// Wait is the sole caller (per the CarrierProcess contract); its
		// return means the carrier has exited and been reaped. Signalling
		// carrierDone here is what lets reapCarrier guarantee the kernel PID
		// is gone before `wait` unblocks.
		sig := 0
		if stopAware, ok := cp.(StopAwareCarrierProcess); ok {
			for {
				var state CarrierWaitState
				if live, ok := cp.(SignalAwareCarrierProcess); ok {
					event := live.WaitSignalState()
					if event.Delivered {
						bg.deliverCarrierSignal(event.Signal)
						continue
					}
					state = event.CarrierWaitState
				} else {
					state = stopAware.WaitState()
				}
				if !state.Stopped {
					sig = state.Signal
					break
				}
				// A grouped carrier is a stable proxy leader, not the job's
				// executable body. Keep the real children stopped, record that
				// state for jobs/bg/fg, and resume only the proxy so its watcher
				// can observe the next wait state. Ungrouped carriers retain the
				// historical cancel-and-reap behavior below.
				if grouped, ok := cp.(ProcessGroupCarrierProcess); ok && bg.pgrpFixed {
					if _, name, ok := signalByNumber(state.Signal); ok {
						bg.setStopSignal("SIG" + name)
					}
					bg.setState(jobStopped)
					if err := grouped.ResumeProcessGroupLeader(); err == nil {
						continue
					}
				}
				// A stopped carrier cannot reach another wait state by itself.
				// Preserve the stop as the job's status, cancel the represented
				// job immediately, then wait until Terminate makes the carrier
				// terminal and the host reaps it.
				if !bg.carrierReaped.Load() {
					bg.killedSignal.CompareAndSwap(0, int32(state.Signal))
					bg.cancel()
				}
				cp.Terminate()
			}
		} else {
			sig = cp.Wait()
		}
		defer close(bg.carrierDone)
		if bg.carrierReaped.Load() {
			return // the job finished first; reapCarrier tore this down
		}
		// The carrier died under a live job: relay the terminating
		// signal per the job's current disposition for it. A trapped
		// signal is queued so the job's own trap machinery runs the
		// action at its next statement boundary (safe even before the
		// job goroutine has started running); an ignored signal leaves
		// the job untouched; a default-disposition signal — or a
		// carrier that somehow exits normally, which must not outlive
		// its kernel identity — kills the job as 128+signal.
		if sig > 0 {
			bg.deliverCarrierSignal(sig)
			return
		}
		bg.cancel()
	}()
	return nil
}

// deliverCarrierSignal classifies one receipt against the current logical
// job, preserving the callback captured with that disposition. A live proxy
// stays available after ignored/trapped deliveries and is reaped by normal
// job cleanup after a default disposition cancels the represented job.
func (bg *bgProc) deliverCarrierSignal(num int) {
	if num <= 0 || bg.carrierReaped.Load() {
		return
	}
	job := bg.carrierSignalRunner.Load()
	if job == nil {
		return
	}
	name, disposition, callback, reset := job.carrierSignalSnapshot(num)
	switch disposition {
	case carrierSigIgnored:
		return
	case carrierSigTrapped:
		job.queuePendingSignal(name, callback)
		return
	}
	if sig, _, ok := signalByNumber(num); ok && signalDefaultDoesNotTerminate(sig) {
		return
	}
	if reset {
		bg.carrierResetSignal.CompareAndSwap(0, int32(num))
	}
	bg.killedSignal.CompareAndSwap(0, int32(num))
	bg.cancel()
}

// carrierIgnoredSignalNames snapshots the real signals the new background
// runner currently treats as ignored. It includes explicit empty traps,
// startup hard ignores, and the implicit INT/QUIT ignores applied to an
// asynchronous list without job control.
func (r *Runner) carrierIgnoredSignalNames() []string {
	seen := make(map[string]bool)
	var names []string
	for _, entry := range killSignals {
		num, ok := signalNumber(entry.Sig)
		if !ok {
			continue
		}
		name, disposition := r.carrierSignalDisposition(num)
		if disposition != carrierSigIgnored || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// carrierSigDisposition classifies a received or terminating carrier signal:
// run a trap action, ignore it, or terminate the represented job.
type carrierSigDisposition int

const (
	carrierSigDefault carrierSigDisposition = iota
	carrierSigIgnored
	carrierSigTrapped
)

// carrierSignalDisposition reports the job runner's current disposition
// for the arrival of signal number num, and the signal's canonical
// name. Called from the carrier watcher goroutine while the job may be
// concurrently changing its traps, so the trapCallbacks read is guarded
// by sigMu (see setTrapCallback). Unknown signals — and SIGKILL and
// SIGSTOP, which are uncatchable no matter what `trap` recorded — are
// always default.
func (r *Runner) carrierSignalDisposition(num int) (string, carrierSigDisposition) {
	name, disposition, _, _ := r.carrierSignalSnapshot(num)
	return name, disposition
}

func (r *Runner) carrierSignalSnapshot(num int) (string, carrierSigDisposition, string, bool) {
	_, name, ok := signalByNumber(num)
	if !ok || name == "KILL" || name == "STOP" {
		return name, carrierSigDefault, "", false
	}
	r.sigMu.Lock()
	defer r.sigMu.Unlock()
	if r.startupIgnored[name] {
		return name, carrierSigIgnored, "", false
	}
	callback, trapped := r.trapCallbacks[name]
	if !trapped {
		return name, carrierSigDefault, "", r.asyncDefaultReset[name]
	}
	if callback == "" {
		return name, carrierSigIgnored, "", false
	}
	return name, carrierSigTrapped, callback, false
}

// reapCarrier tears down the job's carrier process once the job itself
// has finished. Called from the job goroutine before the killedSignal
// read that seals the exit status, so an external kill racing with
// natural completion either lands as 128+signal or is cleanly ignored —
// never half-applied.
//
// It blocks until the carrier has been fully reaped (the watcher's
// [CarrierProcess.Wait] has returned), not merely asked to exit. That
// synchronization closes a lifecycle race: without it `wait` on a
// naturally-completed job could unblock while the carrier lingered as a
// zombie, so a following `kill -0 $!` still saw the PID and a retrying
// `wait` loop hit "pid is not a child". Terminate then Wait keeps the
// single-Wait contract — the watcher goroutine owns the only Wait call —
// while giving reapCarrier a happens-before edge on the reap.
func (bg *bgProc) reapCarrier() {
	if bg.carrier == nil {
		return
	}
	bg.carrierReaped.Store(true)
	bg.carrier.Terminate()
	<-bg.carrierDone
}
