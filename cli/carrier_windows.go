// Copyright (c) 2026 qiangli
// See LICENSE for licensing information

//go:build windows

package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"

	"github.com/qiangli/bash/interp"
)

// platformJobCarrier returns the OS-backed job carrier for Windows: one
// re-exec of this executable in helper mode per background job (see
// MaybeRunJobCarrierHelper), giving every `cmd &` a real, probeable,
// signalable kernel PID for `$!`.
//
// A carrier is pure process identity, and Windows has that: CreateProcess
// hands back a process id, OpenProcess answers `kill -0`, and the shell's own
// `kill` already speaks the Windows dialect of every signal
// (sendsignal_windows.go) — TerminateProcess with bashy's exit-code marker for
// a terminating one, ntdll suspend/resume for a stop. None of fork, waitpid or
// POSIX process groups is needed to be a stand-in process.
//
// What Windows cannot host is the *live signal proxy* carrier_unix.go builds
// on fd 3: os/exec rejects ExtraFiles there, so there is no private inherited
// descriptor to relay catchable signals over. This is therefore a basic
// [interp.JobCarrier], the contract WithJobCarrier documents for one: the
// carrier keeps its default dispositions, an external `kill` decides the job's
// fate by killing the carrier, and the runner relays that signal to the job
// when it reaps it — still running the job's trap, still honouring an ignore,
// still killing an untrapped job as 128+signal. The one thing it gives up is
// surviving a trapped or ignored signal: after the first `kill $!` the carrier
// PID is gone, while the job itself carries on if its disposition says so.
func platformJobCarrier() interp.JobCarrier { return winJobCarrier{} }

// The readiness/event descriptor is a Unix mechanism (see carrier_unix.go).
// A Windows carrier installs no handler to become usable — its PID is live and
// signalable the moment CreateProcess returns — and has no descriptor to
// publish readiness on, so both hooks are empty here.
func resetJobCarrierSignals() {}
func signalJobCarrierReady()  {}

type winJobCarrier struct{}

func (winJobCarrier) StartCarrier(ctx context.Context) (interp.CarrierProcess, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolving executable: %v", err)
	}
	cmd := exec.Command(exe, carrierHelperArg)
	// A carrier must inherit no user-facing configuration: it is intercepted
	// before flag parsing, startup files and telemetry, and BASH_ENV or OTEL_*
	// must never reach it. os/exec supplies SYSTEMROOT itself on Windows, which
	// is the one variable the child genuinely needs.
	cmd.Env = []string{}
	// The Windows counterpart of the Unix carrier's own process group: a
	// console ^C raises CTRL_C_EVENT in every process of the console's group,
	// and a carrier is not part of the shell's foreground job.
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		stdin.Close()
		return nil, err
	}
	return &winCarrierProc{cmd: cmd, stdin: stdin}, nil
}

type winCarrierProc struct {
	cmd   *exec.Cmd
	stdin io.Closer
	term  sync.Once
}

func (p *winCarrierProc) Pid() int { return p.cmd.Process.Pid }

// Wait reaps the helper and reports the signal that killed it, 0 for an
// ordinary exit. Windows has no wait status: a terminated process carries only
// the exit code its killer chose, so the signal is recovered from the marker
// bashy's own `kill` encodes into it (interp.SignalFromMarkerExitCode).
// Terminate's plain TerminateProcess — and anything else that ends the carrier
// without the marker — reads as an ordinary exit, which is what it is.
//
// The runner calls it exactly once.
func (p *winCarrierProc) Wait() int {
	_ = p.cmd.Wait()
	st := p.cmd.ProcessState
	if st == nil {
		return 0
	}
	if num, ok := interp.SignalFromMarkerExitCode(st.ExitCode()); ok {
		return num
	}
	return 0
}

// Terminate makes the helper exit promptly: EOF on its stdin pipe is its exit
// condition, and the kill spares waiting for it to notice. Idempotent via the
// sync.Once, and safe to race with Wait — os.Process keeps Kill after reap a
// harmless ErrProcessDone. A carrier suspended by `kill -STOP` is terminable
// as it stands; TerminateProcess does not need the target to be running, so
// there is no Unix-style SIGCONT to undo the stop first.
func (p *winCarrierProc) Terminate() {
	p.term.Do(func() {
		p.stdin.Close()
		_ = p.cmd.Process.Kill()
	})
}
