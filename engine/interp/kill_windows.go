// Copyright (c) 2026, the outpost authors
// See LICENSE for licensing information

//go:build windows

package interp

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"syscall"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// killSig on Windows carries the Cygwin/MSYS signal number and the os.Signal
// value used for os/signal bookkeeping and the in-process bus. Windows has
// no kernel signals; see signal_wintable.go for the model.
type killSig struct {
	Name   string
	Num    int
	Signal os.Signal
}

func sigIsZero(s killSig) bool { return s.Num == 0 }
func sigNum(s killSig) int     { return s.Num }

func targetsOwnProcessGroup(int) bool { return false }

const defaultTermSignalNum = windowsSigTERM

var defaultTermSignal = killSig{Name: "TERM", Num: defaultTermSignalNum, Signal: syscall.Signal(defaultTermSignalNum)}

// killSignals is the full bash signal table under Cygwin/MSYS numbering, in
// `kill -l` listing order. Every entry is a syscall.Signal(n): os/signal on
// Windows accepts any n < 65 (its enable/disable/ignore hooks are empty
// bookkeeping), which is what lets the trap machinery in signal.go work
// unchanged, with delivery coming from processSignalBus instead of the kernel.
var killSignals = windowsKillSignals()

func windowsKillSignals() []struct {
	Name string
	Sig  killSig
} {
	table := windowsSignalTable()
	sigs := make([]struct {
		Name string
		Sig  killSig
	}, len(table))
	for i, e := range table {
		sig := os.Signal(syscall.Signal(e.Num))
		if e.Num == windowsSigINT {
			sig = os.Interrupt
		}
		sigs[i].Name = e.Name
		sigs[i].Sig = killSig{Name: e.Name, Num: e.Num, Signal: sig}
	}
	return sigs
}

// signalByName resolves a bash-style signal name to its table entry.
// Case-insensitive; accepts both "TERM" and "SIGTERM".
func signalByName(name string) (killSig, bool) {
	e, ok := windowsSignalByName(name)
	if !ok {
		return killSig{}, false
	}
	return killSignals[e.Num-1].Sig, true
}

// signalByNumber resolves a numeric signal to a known entry. Signal 0 is the
// POSIX "no-op probe" — returned as-is so `kill -0 PID` works for existence
// checks even though 0 is not in the table.
func signalByNumber(n int) (killSig, string, bool) {
	if n == 0 {
		return killSig{Name: "EXIT", Num: 0}, "EXIT", true
	}
	e, ok := windowsSignalByNumber(n)
	if !ok {
		return killSig{}, "", false
	}
	sig := killSignals[e.Num-1].Sig
	return sig, sig.Name, true
}

// sortedSignalEntries returns the entries in numerical order for `kill -l`.
func sortedSignalEntries() []struct {
	Name string
	Sig  killSig
} {
	return killSignals
}

func signalNumber(sig killSig) (int, bool) {
	if sig.Name == "" && sig.Num != 0 {
		return 0, false
	}
	return sig.Num, true
}

func signalName(sig killSig) (string, bool) {
	if sig.Name == "" && sig.Num != 0 {
		return "", false
	}
	return sig.Name, true
}

func signalForOS(sig killSig) os.Signal {
	return sig.Signal
}

// notifyForegroundSignalDeath prints bash's status line for a FOREGROUND
// external command that was killed by a fatal signal, as kill_unix.go does:
// non-interactive shells only, silent in POSIX mode and for INT/PIPE. A
// Windows wait status is decoded from the bashy signal marker
// (waitstatus_windows.go), which never carries a core-dump flag, so only the
// bare "<description> <cmd>" form applies.
func (r *Runner) notifyForegroundSignalDeath(w io.Writer, pos syntax.Pos, pid int, status waitStatus, args []string) {
	if r == nil || r.opts[optPosix] || r.interactiveShell {
		return
	}
	if line, ok := windowsSignalDeathNotice(status.Signal(), args); ok {
		fmt.Fprintln(w, line)
	}
}

// continueIfStopped resumes a process this shell suspended for a stop
// signal, best-effort. Errors are swallowed because the process may already
// be running or gone, as in kill_unix.go.
func continueIfStopped(pid int) {
	_ = resumeProcess(pid)
}

func jobSignalPid(bg *bgProc) int {
	return int(bg.pid.Load())
}

// foregroundContinuePid is the kill target `fg` resumes a job with. Windows
// has no process groups in the POSIX sense and never records one for a job
// (recordBackgroundProcessGroup is a no-op there), so `fg` resumes the job's
// own process, which NtResumeProcess restarts in full.
func foregroundContinuePid(bg *bgProc) int {
	return jobSignalPid(bg)
}

func signalStopsJob(sig killSig) bool { return windowsSignalStopsJob(sig.Num) }

func signalContinuesJob(sig killSig) bool { return windowsSignalContinuesJob(sig.Num) }

func signalDefaultDoesNotTerminate(sig killSig) bool {
	return windowsSignalDefaultDoesNotTerminate(sig.Num)
}

// parseSignalSpec parses the part after the leading `-` in `kill -SPEC pid…`.
func parseSignalSpec(spec string) (killSig, bool) {
	if n, err := strconv.Atoi(spec); err == nil {
		sig, _, ok := signalByNumber(n)
		return sig, ok
	}
	return signalByName(spec)
}
