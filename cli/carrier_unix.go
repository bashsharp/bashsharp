// Copyright (c) 2026 qiangli
// See LICENSE for licensing information

//go:build unix

package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/bashsharp/bashsharp/engine/interp"
)

// platformJobCarrier returns the OS-backed job carrier: one re-exec of this
// executable in helper mode per background job (see MaybeRunJobCarrierHelper),
// giving every `cmd &` a real, signalable kernel PID for `$!`.
func platformJobCarrier() interp.JobCarrier { return execJobCarrier{} }

// Used only in the isolated helper process. The readiness descriptor remains
// open afterward as a one-byte signal-event stream; no user code inherits it.
var carrierSignalPipe *os.File
var carrierSignalReady chan struct{}

func resetJobCarrierSignals() {
	if os.Getenv(carrierReadyEnv) == "3" {
		carrierSignalPipe = os.NewFile(3, "bashy-job-carrier-events")
		carrierSignalReady = make(chan struct{})
	}
	// The helper is a signal proxy, not the shell executing the job. With
	// the private event stream it reports catchable signals without exiting;
	// the host applies its live disposition. Direct legacy helper invocations
	// retain the prior ignored-snapshot and 128+signal exit behavior.

	sigs := carrierSignalNumbers
	ignored := make(map[string]bool)
	for _, name := range strings.Split(os.Getenv(carrierIgnoredEnv), ",") {
		ignored[name] = name != ""
	}
	targets := make([]os.Signal, 0, len(sigs))
	for _, entry := range sigs {
		stopSignal := entry.sig == syscall.SIGTSTP || entry.sig == syscall.SIGTTIN || entry.sig == syscall.SIGTTOU
		if carrierSignalPipe != nil && !stopSignal {
			// This process represents a mutable shell job. The host applies
			// its current disposition, including immutable startup ignores.
			targets = append(targets, entry.sig)
			continue
		}
		if ignored[entry.name] {
			signal.Ignore(entry.sig)
			continue
		}
		if !signal.Ignored(entry.sig) {
			targets = append(targets, entry.sig)
		}
	}
	if len(targets) == 0 {
		return
	}
	// Reserve room for every distinct supported pending signal while the
	// event writer is blocked; repeated standard signals may coalesce.
	ch := make(chan os.Signal, len(carrierSignalNumbers))
	signal.Notify(ch, targets...)
	go func() {
		if carrierSignalReady != nil {
			<-carrierSignalReady
		}
		for sig := range ch {
			num, ok := sig.(syscall.Signal)
			if !ok {
				os.Exit(255)
			}
			switch num {
			case syscall.SIGTSTP, syscall.SIGTTIN, syscall.SIGTTOU:
				// Preserve job-control stop/resume visibility. SIGSTOP cannot
				// be caught; a later SIGCONT resumes this relay loop.
				_ = syscall.Kill(os.Getpid(), syscall.SIGSTOP)
			default:
				if carrierSignalPipe != nil {
					if _, err := carrierSignalPipe.Write([]byte{byte(num)}); err != nil {
						os.Exit(1)
					}
					continue
				}
				os.Exit(128 + int(num))
			}
		}
	}()
}

func signalJobCarrierReady() {
	if carrierSignalPipe == nil {
		return
	}
	if _, err := carrierSignalPipe.Write([]byte{1}); err != nil {
		os.Exit(1)
	}
	close(carrierSignalReady)
}

type execJobCarrier struct{}

func (execJobCarrier) StartCarrier(ctx context.Context) (interp.CarrierProcess, error) {
	return startExecJobCarrier(ctx, nil)
}

func (execJobCarrier) StartCarrierWithIgnoredSignals(ctx context.Context, ignored []string) (interp.CarrierProcess, error) {
	return startExecJobCarrier(ctx, ignored)
}

func startExecJobCarrier(ctx context.Context, ignored []string) (interp.CarrierProcess, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolving executable: %v", err)
	}
	cmd := exec.Command(exe, carrierHelperArg)
	// Only the private readiness descriptor marker is inherited. The helper is
	// intercepted before flag parsing, startup files and telemetry; user-facing
	// environment such as BASH_ENV, BASH_SETPGRP and OTEL_* must not reach it.
	cmd.Env = []string{carrierReadyEnv + "=3"}
	if len(ignored) > 0 {
		cmd.Env = append(cmd.Env, carrierIgnoredEnv+"="+strings.Join(ignored, ","))
	}
	// Its own process group: under job control (`set -m`) group-directed
	// signals target the negated carrier PID, and a tty ^C aimed at the
	// shell's foreground group must not strike carriers.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	readyR, readyW, err := os.Pipe()
	if err != nil {
		stdin.Close()
		return nil, err
	}
	cmd.ExtraFiles = []*os.File{readyW}
	// Snapshot the actual inherited disposition before starting the helper.
	// Standalone default restoration can deliberately leave os/signal's Go
	// bookkeeping ignored while the kernel is SIG_DFL.
	initialIgnored := make(map[int]bool)
	for _, entry := range carrierSignalNumbers {
		if (interp.OSSignalResetter{}).IsIgnored(int(entry.sig), entry.name) {
			initialIgnored[int(entry.sig)] = true
		}
	}
	for _, name := range ignored {
		for _, entry := range carrierSignalNumbers {
			if entry.name == name {
				initialIgnored[int(entry.sig)] = true
			}
		}
	}
	if err := cmd.Start(); err != nil {
		stdin.Close()
		readyR.Close()
		readyW.Close()
		return nil, err
	}
	readyW.Close()
	_ = readyR.SetReadDeadline(time.Now().Add(5 * time.Second))
	var ready [1]byte
	_, readyErr := io.ReadFull(readyR, ready[:])
	if readyErr == nil && ready[0] != 1 {
		readyErr = fmt.Errorf("invalid readiness byte")
	}
	if readyErr == nil {
		readyErr = readyR.SetReadDeadline(time.Time{})
	}
	if readyErr != nil {
		readyR.Close()
		stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("job carrier readiness: %v", readyErr)
	}
	return &execCarrierProc{cmd: cmd, pid: cmd.Process.Pid, stdin: stdin, events: readyR, initialIgnored: initialIgnored}, nil
}

var carrierSignalNumbers = []struct {
	name string
	sig  syscall.Signal
}{
	{"HUP", syscall.SIGHUP}, {"INT", syscall.SIGINT}, {"QUIT", syscall.SIGQUIT}, {"ILL", syscall.SIGILL}, {"TRAP", syscall.SIGTRAP}, {"ABRT", syscall.SIGABRT}, {"BUS", syscall.SIGBUS}, {"FPE", syscall.SIGFPE}, {"USR1", syscall.SIGUSR1}, {"SEGV", syscall.SIGSEGV}, {"USR2", syscall.SIGUSR2}, {"PIPE", syscall.SIGPIPE}, {"ALRM", syscall.SIGALRM}, {"TERM", syscall.SIGTERM}, {"TSTP", syscall.SIGTSTP}, {"TTIN", syscall.SIGTTIN}, {"TTOU", syscall.SIGTTOU}, {"XCPU", syscall.SIGXCPU}, {"XFSZ", syscall.SIGXFSZ},
}

type execCarrierProc struct {
	cmd            *exec.Cmd
	pid            int
	stdin          io.Closer
	term           sync.Once
	reaped         atomic.Bool
	cleanup        sync.Once
	events         *os.File
	initialIgnored map[int]bool
	waitOnce       sync.Once
	signals        chan int
	states         chan interp.CarrierWaitState
	terminal       *interp.CarrierWaitState
}

func (p *execCarrierProc) Pid() int { return p.pid }

// ProcessGroupID advertises the stable group created by Setpgid at carrier
// startup. The sh runner uses it only while monitor mode is active, placing
// every external component of the represented job in the carrier's group.
func (p *execCarrierProc) ProcessGroupID() int { return p.pid }

func (p *execCarrierProc) ResumeProcessGroupLeader() error {
	if p.reaped.Load() {
		return os.ErrProcessDone
	}
	return syscall.Kill(p.pid, syscall.SIGCONT)
}

// WaitState reaps or observes child stop state via wait4/waitpid with WUNTRACED.
func (p *execCarrierProc) waitKernelState() interp.CarrierWaitState {
	pid := p.pid
	var ws syscall.WaitStatus
	for {
		_, err := syscall.Wait4(pid, &ws, syscall.WUNTRACED, nil)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			if p.cmd.ProcessState != nil {
				if ws, ok := p.cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
					return interp.CarrierWaitState{Signal: int(ws.Signal()), Stopped: false}
				}
				if code := p.cmd.ProcessState.ExitCode(); code > 128 && code < 256 {
					return interp.CarrierWaitState{Signal: code - 128, Stopped: false}
				}
			}
			return interp.CarrierWaitState{Signal: 0, Stopped: false}
		}
		break
	}

	if sig, stopped := isWaitStatusStopped(ws); stopped {
		return interp.CarrierWaitState{
			Signal:  sig,
			Stopped: true,
		}
	}

	if ws.Signaled() {
		return interp.CarrierWaitState{Signal: int(ws.Signal()), Stopped: false}
	}
	if code := ws.ExitStatus(); code > 128 && code < 256 {
		return interp.CarrierWaitState{Signal: code - 128, Stopped: false}
	}
	return interp.CarrierWaitState{Signal: 0, Stopped: false}
}

// WaitSignalState has one consumer. The wait4 owner and pipe reader are
// separate because an ignored/trapped signal does not produce a kernel wait
// status. A terminal wait result is withheld until the event pipe reaches EOF,
// so preceding events are drained and both reader goroutines have finished.
func (p *execCarrierProc) WaitSignalState() interp.CarrierSignalState {
	p.waitOnce.Do(func() {
		p.signals = make(chan int)
		p.states = make(chan interp.CarrierWaitState, 1)
		go func() {
			defer close(p.states)
			for {
				state := p.waitKernelState()
				if !state.Stopped {
					p.reaped.Store(true)
				}
				p.states <- state
				if !state.Stopped {
					return
				}
			}
		}()
		go func() {
			defer close(p.signals)
			defer p.events.Close()
			var event [1]byte
			for {
				if _, err := io.ReadFull(p.events, event[:]); err != nil {
					p.Terminate() // an unexpectedly closed protocol cannot leave a live proxy
					return
				}
				if event[0] == 0 || event[0] >= 128 {
					p.Terminate()
					return
				}
				p.signals <- int(event[0])
			}
		}()
	})
	for {
		if p.terminal != nil {
			if p.signals != nil {
				if sig, ok := <-p.signals; ok {
					return interp.CarrierSignalState{Delivered: true, CarrierWaitState: interp.CarrierWaitState{Signal: sig}}
				}
				p.signals = nil
			}
			state := *p.terminal
			<-p.states // join the single kernel waiter after its terminal publication
			p.cleanup.Do(func() {
				// wait4 consumed the terminal status for stop-aware job control.
				// exec.Cmd.Wait still owns its parent pipes, and os.Process owns a
				// Linux pidfd. Wait reports ECHILD here, but closes the pipes;
				// Release then closes the pidfd. Keep p.pid stable for callers.
				_ = p.cmd.Wait()
				_ = p.cmd.Process.Release()
			})
			return interp.CarrierSignalState{CarrierWaitState: state}
		}
		select {
		case sig, ok := <-p.signals:
			if !ok {
				p.signals = nil
				continue
			}
			return interp.CarrierSignalState{Delivered: true, CarrierWaitState: interp.CarrierWaitState{Signal: sig}}
		case state := <-p.states:
			if state.Stopped {
				return interp.CarrierSignalState{CarrierWaitState: state}
			}
			p.terminal = &state
		}
	}
}

// Preserve the old terminal/stop-only API for callers which do not opt into
// live delivery. Its immutable startup ignore snapshot remains authoritative.
func (p *execCarrierProc) WaitState() interp.CarrierWaitState {
	for {
		state := p.WaitSignalState()
		if !state.Delivered {
			return state.CarrierWaitState
		}
		if p.initialIgnored[state.Signal] {
			continue
		}
		p.Terminate()
		for {
			final := p.WaitSignalState()
			if !final.Delivered && !final.Stopped {
				return interp.CarrierWaitState{Signal: state.Signal}
			}
		}
	}
}

// Wait reaps the helper and maps a signal death to its signal number, 0 for a
// normal exit. The runner calls it exactly once.
func (p *execCarrierProc) Wait() int {
	for {
		st := p.WaitState()
		if !st.Stopped {
			return st.Signal
		}
	}
}

// Terminate makes the helper exit promptly: EOF on its stdin pipe is its exit
// condition, and the kill spares waiting for it to notice. Idempotent via the
// sync.Once; the reaped guard avoids signalling a recycled PID after wait4.
func (p *execCarrierProc) Terminate() {
	p.term.Do(func() {
		p.stdin.Close()
		if p.reaped.Load() {
			return
		}
		_ = p.cmd.Process.Kill()
	})
}

func isWaitStatusStopped(ws syscall.WaitStatus) (int, bool) {
	if ws.Stopped() {
		return int(ws.StopSignal()), true
	}
	u := uint32(ws)
	if u&0xff == 0x7f {
		sig := int((u >> 8) & 0xff)
		if sig > 0 && sig != 0x7f {
			return sig, true
		}
	}
	return 0, false
}
