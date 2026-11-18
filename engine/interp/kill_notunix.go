// Copyright (c) 2026, the outpost authors
// See LICENSE for licensing information

//go:build !unix && !windows

package interp

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

type killSig struct {
	Name   string
	Num    int
	Signal os.Signal
}

func sigIsZero(s killSig) bool { return s.Num == 0 }
func sigNum(s killSig) int     { return s.Num }

func targetsOwnProcessGroup(int) bool { return false }

const defaultTermSignalNum = 15

var defaultTermSignal = killSig{Name: "TERM", Num: defaultTermSignalNum, Signal: os.Kill}

// killSignals on non-Unix is the small set of signals the Go runtime can
// actually deliver via os.Process.Signal — Interrupt and Kill. SIGTERM is
// included because scripts portably use it; it's delivered as os.Kill where
// Windows where there is no graceful equivalent.
var killSignals = []struct {
	Name string
	Sig  killSig
}{
	{"INT", killSig{Name: "INT", Num: 2, Signal: os.Interrupt}},
	{"KILL", killSig{Name: "KILL", Num: 9, Signal: os.Kill}},
	{"TERM", defaultTermSignal},
}

func signalByName(name string) (killSig, bool) {
	name = strings.ToUpper(name)
	name = strings.TrimPrefix(name, "SIG")
	for _, e := range killSignals {
		if e.Name == name {
			return e.Sig, true
		}
	}
	return killSig{}, false
}

func signalByNumber(n int) (killSig, string, bool) {
	if n == 0 {
		return killSig{Name: "EXIT", Num: 0}, "EXIT", true
	}
	for _, e := range killSignals {
		if e.Sig.Num == n {
			return e.Sig, e.Name, true
		}
	}
	return killSig{}, "", false
}

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

// notifyForegroundSignalDeath is a no-op on non-Unix platforms: waitStatus
// there cannot report Signaled()/Signal()/CoreDump(), so the foreground
// signal-death notification (#25/#26) never applies.
func (r *Runner) notifyForegroundSignalDeath(w io.Writer, pos syntax.Pos, pid int, status waitStatus, args []string) {
}

// continueIfStopped is a no-op on non-Unix: there is no SIGCONT analog,
// and this runner cannot suspend jobs on this platform anyway.
func continueIfStopped(pid int) {}

func jobSignalPid(bg *bgProc) int {
	return int(bg.pid.Load())
}

// foregroundContinuePid is the kill target `fg` resumes a job with. There
// are no process groups to resume off unix, so it names the process itself;
// foregroundExistingJob refuses here anyway.
func foregroundContinuePid(bg *bgProc) int {
	return jobSignalPid(bg)
}

func signalStopsJob(sig killSig) bool { return false }

func signalContinuesJob(sig killSig) bool { return false }

func signalDefaultDoesNotTerminate(sig killSig) bool { return false }

// sendSignal on non-Unix uses os.Process.Signal which only supports
// Interrupt and Kill. SIGTERM is mapped to Kill (no graceful equivalent
// exists on Windows). Signal 0 does an existence probe via os.FindProcess.
func sendSignal(pid int, sig killSig) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	switch sig.Num {
	case 0:
		// Best-effort existence probe; FindProcess on Windows almost
		// always succeeds even for non-existent PIDs, so this is weak.
		return nil
	case 2:
		return proc.Signal(sig.Signal)
	case 9, defaultTermSignalNum:
		return proc.Kill()
	default:
		return fmt.Errorf("signal %d not supported on this platform", sig.Num)
	}
}

func parseSignalSpec(spec string) (killSig, bool) {
	if n, err := strconv.Atoi(spec); err == nil {
		sig, _, ok := signalByNumber(n)
		return sig, ok
	}
	return signalByName(spec)
}
