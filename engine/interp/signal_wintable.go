// Copyright (c) 2026, the outpost authors
// See LICENSE for licensing information

package interp

import (
	"bytes"
	"strconv"
	"strings"
)

// This file holds the platform-neutral half of the Windows signal model so
// its logic is testable on any host: the Cygwin/MSYS-numbered signal table,
// the bashy-owned "died of signal N" exit-code marker, the wait status that
// decodes it, and the wire format of the bashy-to-bashy signal pipe. The
// Windows build (kill_windows.go, sendsignal_windows.go,
// signalserver_windows.go, waitstatus_windows.go) only adds the syscalls.

// windowsSignalEntry is one row of the Windows signal table.
type windowsSignalEntry struct {
	Name string
	Num  int
}

// Cygwin/MSYS realtime signal range. bash on Windows (MSYS2/Cygwin) numbers
// SIGRTMIN=32 and SIGRTMAX=64, unlike glibc's 34..64.
const (
	windowsRTMin = 32
	windowsRTMax = 64
)

// windowsSignalTable is the full bash signal table under Cygwin/MSYS
// numbering, in `kill -l` listing order. Every entry maps to
// syscall.Signal(Num) on Windows; os/signal there does pure bookkeeping for
// any number below 65, so the whole table can be trapped, ignored and reset
// through the existing os/signal-shaped machinery, with delivery supplied by
// the in-process bus (signalbus.go).
func windowsSignalTable() []windowsSignalEntry {
	sigs := []windowsSignalEntry{
		{"HUP", 1}, {"INT", 2}, {"QUIT", 3}, {"ILL", 4}, {"TRAP", 5},
		{"ABRT", 6}, {"EMT", 7}, {"FPE", 8}, {"KILL", 9}, {"BUS", 10},
		{"SEGV", 11}, {"SYS", 12}, {"PIPE", 13}, {"ALRM", 14}, {"TERM", 15},
		{"URG", 16}, {"STOP", 17}, {"TSTP", 18}, {"CONT", 19}, {"CHLD", 20},
		{"TTIN", 21}, {"TTOU", 22}, {"IO", 23}, {"XCPU", 24}, {"XFSZ", 25},
		{"VTALRM", 26}, {"PROF", 27}, {"WINCH", 28}, {"PWR", 29}, {"USR1", 30},
		{"USR2", 31},
	}
	for i := windowsRTMin; i <= windowsRTMax; i++ {
		sigs = append(sigs, windowsSignalEntry{rtSignalName(i, windowsRTMin, windowsRTMax), i})
	}
	return sigs
}

// windowsSignalByName resolves a bash-style signal name in the Windows
// table. Case-insensitive; accepts both "TERM" and "SIGTERM".
func windowsSignalByName(name string) (windowsSignalEntry, bool) {
	name = strings.ToUpper(name)
	name = strings.TrimPrefix(name, "SIG")
	for _, e := range windowsSignalTable() {
		if e.Name == name {
			return e, true
		}
	}
	return windowsSignalEntry{}, false
}

// windowsSignalByNumber resolves a signal number in the Windows table.
func windowsSignalByNumber(n int) (windowsSignalEntry, bool) {
	for _, e := range windowsSignalTable() {
		if e.Num == n {
			return e, true
		}
	}
	return windowsSignalEntry{}, false
}

// windowsSignalDeathNotice is the bash status line for a foreground command
// killed by signal num under the Windows table ("Terminated <cmd>"); ok is
// false for the signals bash does not announce (INT, PIPE) and for signals
// without a description.
func windowsSignalDeathNotice(num int, args []string) (string, bool) {
	if windowsSignalDeathSilent(num) {
		return "", false
	}
	e, ok := windowsSignalByNumber(num)
	if !ok {
		return "", false
	}
	desc, ok := signalDescriptions[e.Name]
	if !ok {
		return "", false
	}
	return desc + " " + strings.Join(args, " "), true
}

// windowsSignalListEntries renders the Windows table for formatSignalList.
func windowsSignalListEntries() []signalListEntry {
	table := windowsSignalTable()
	entries := make([]signalListEntry, len(table))
	for i, e := range table {
		entries[i] = signalListEntry{Num: e.Num, Name: e.Name}
	}
	return entries
}

// Windows signal numbers the job-control and self-signal logic special-cases.
const (
	windowsSigINT   = 2
	windowsSigKILL  = 9
	windowsSigPIPE  = 13
	windowsSigTERM  = 15
	windowsSigURG   = 16
	windowsSigSTOP  = 17
	windowsSigTSTP  = 18
	windowsSigCONT  = 19
	windowsSigCHLD  = 20
	windowsSigTTIN  = 21
	windowsSigTTOU  = 22
	windowsSigWINCH = 28
)

// windowsSignalStopsJob mirrors kill_unix.go's signalStopsJob under Windows
// numbering: STOP, TSTP, TTIN and TTOU suspend a job.
func windowsSignalStopsJob(num int) bool {
	switch num {
	case windowsSigSTOP, windowsSigTSTP, windowsSigTTIN, windowsSigTTOU:
		return true
	}
	return false
}

// windowsSignalContinuesJob reports whether num is SIGCONT.
func windowsSignalContinuesJob(num int) bool { return num == windowsSigCONT }

// windowsSignalDefaultDoesNotTerminate reports the signals whose default
// action is not process death: the ignored-by-default set (CHLD, URG, WINCH),
// CONT, and the stop set.
func windowsSignalDefaultDoesNotTerminate(num int) bool {
	switch num {
	case windowsSigCHLD, windowsSigCONT, windowsSigURG, windowsSigWINCH:
		return true
	}
	return windowsSignalStopsJob(num)
}

// windowsSignalAction is what sendSignal does to another process once the
// target's bashy signal pipe has declined the signal, or been skipped. It is
// defined here, away from the syscalls, so the routing is unit-testable on
// any host.
type windowsSignalAction int

const (
	// windowsActionTerminate ends the process with the signal marker.
	windowsActionTerminate windowsSignalAction = iota
	// windowsActionSuspend stops every thread (NtSuspendProcess).
	windowsActionSuspend
	// windowsActionResume restarts a stopped process (NtResumeProcess).
	windowsActionResume
	// windowsActionProbe only checks that the process is still there: the
	// signal's default action is neither death nor a state change.
	windowsActionProbe
)

// windowsSignalActionFor classifies num for sendSignal.
func windowsSignalActionFor(num int) windowsSignalAction {
	switch {
	case windowsSignalStopsJob(num):
		return windowsActionSuspend
	case windowsSignalContinuesJob(num):
		return windowsActionResume
	case windowsSignalDefaultDoesNotTerminate(num):
		return windowsActionProbe
	}
	return windowsActionTerminate
}

// windowsSignalBypassesPipe reports the signals that are never offered to a
// sibling bashy over its signal pipe (signalserver_windows.go) but always
// act on the process directly.
//
// KILL cannot be caught, as on Unix. The job-control set is excluded for two
// reasons: a stop is a change of process state rather than an event a
// program can respond to, and the receiving bashy's default action for an
// untrapped signal is to exit with the signal marker — delivering STOP down
// the pipe would kill the job instead of stopping it. CONT must bypass it
// too, since a suspended process cannot serve its own pipe: the write would
// block until the very resume it is asking for.
//
// The cost is that a sibling bashy's `trap ... TSTP` is not run by another
// shell's `kill -TSTP`; a self-directed `kill -TSTP $$` still goes through
// the in-process bus and fires the trap.
func windowsSignalBypassesPipe(num int) bool {
	if num == windowsSigKILL {
		return true
	}
	return windowsSignalStopsJob(num) || windowsSignalContinuesJob(num)
}

// windowsSignalDeathSilent reports the signals whose foreground death bash
// does not announce (INT and PIPE), see notifyForegroundSignalDeath.
func windowsSignalDeathSilent(num int) bool {
	return num == windowsSigINT || num == windowsSigPIPE
}

// Windows has no wait status: a process only ever reports a 32-bit exit
// code. bashy therefore owns an exit-code marker for "terminated by signal
// N": TerminateProcess uses it when the shell kills an external child, and a
// bashy process that dies of a default-action signal exits with it, so a
// bashy parent decodes 128+N and prints "Terminated" exactly as on Unix. The
// high half is an arbitrary constant no ordinary program returns; the low
// byte carries the signal number. A plain `exit 143` never matches.
const (
	signalMarkerBase = 0x7E5A0000
	signalMarkerMask = 0xFFFFFF00
)

// encodeSignalMarker returns the marker exit code for signal num.
func encodeSignalMarker(num int) uint32 {
	return uint32(signalMarkerBase | (num & 0xFF))
}

// decodeSignalMarker extracts the signal number from a marker exit code.
// ok is false for any code outside the marker range or with a signal number
// outside the table.
func decodeSignalMarker(code uint32) (num int, ok bool) {
	if code&signalMarkerMask != signalMarkerBase {
		return 0, false
	}
	num = int(code & 0xFF)
	if num < 1 || num > windowsRTMax {
		return 0, false
	}
	return num, true
}

// SignalMarkerExitCode is the exit code a standalone Windows shell must exit
// with when it dies of a default-action signal num, so that a parent bashy
// reports the child as killed by that signal (see StartProcessSignalServer
// and SetProcessSignalDefault). Off Windows it is unused; a Unix host
// re-raises the real signal instead.
func SignalMarkerExitCode(num int) int {
	return int(encodeSignalMarker(num))
}

// SignalFromMarkerExitCode is the inverse of [SignalMarkerExitCode]: it
// reports the signal a Windows exit code encodes, and false for any code
// that is not a marker — including a plain `exit 143`, which is an ordinary
// exit and never SIGTERM.
//
// The interpreter applies this judgement to its own children internally (see
// execWaitStatus); this exports it for a host that reaps a bashy-owned child
// itself and must tell the interpreter how it died. A Windows job carrier is
// the case in point: the shell's `kill` terminates the carrier process with
// the marker, and the host's [CarrierProcess.Wait] has only that exit code to
// recover the signal number from.
func SignalFromMarkerExitCode(code int) (num int, ok bool) {
	return decodeSignalMarker(uint32(code))
}

// windowsWaitStatus is the Windows waitStatus: a decoded terminating signal,
// or none. It is a distinct type from the Unix syscall.WaitStatus alias but
// offers the same Signaled/Signal/CoreDump surface the handler uses.
type windowsWaitStatus struct {
	sig int
}

func (w windowsWaitStatus) Signaled() bool { return w.sig > 0 }
func (w windowsWaitStatus) Signal() int    { return w.sig }
func (w windowsWaitStatus) CoreDump() bool { return false }

// decodeWindowsWaitStatus derives the wait status of a reaped child from the
// signal the shell recorded when it terminated that pid (0 when none) and the
// child's raw exit code. A recorded signal wins; otherwise only the bashy
// marker identifies a signal death. ok is false when the child simply
// exited, so a plain 143 is never reported as SIGTERM.
func decodeWindowsWaitStatus(recorded int, exitCode uint32) (windowsWaitStatus, bool) {
	if recorded > 0 {
		return windowsWaitStatus{sig: recorded}, true
	}
	if num, ok := decodeSignalMarker(exitCode); ok {
		return windowsWaitStatus{sig: num}, true
	}
	return windowsWaitStatus{}, false
}

// signalPipeName is the per-process named pipe a bashy process serves so a
// sibling bashy can deliver a signal to it (see StartProcessSignalServer).
func signalPipeName(pid int) string {
	return `\\.\pipe\bashy-sig-` + strconv.Itoa(pid)
}

// encodeSignalMessage is the wire format of the signal pipe: the decimal
// signal number followed by a newline.
func encodeSignalMessage(num int) []byte {
	return []byte(strconv.Itoa(num) + "\n")
}

// decodeSignalMessage parses one pipe message. Trailing whitespace is
// tolerated; anything that is not a signal number in the table is rejected.
func decodeSignalMessage(msg []byte) (num int, ok bool) {
	msg = bytes.TrimSpace(msg)
	n, err := strconv.Atoi(string(msg))
	if err != nil || n < 1 || n > windowsRTMax {
		return 0, false
	}
	return n, true
}
