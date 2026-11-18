// Copyright (c) 2026, the outpost authors
// See LICENSE for licensing information

//go:build windows

package interp

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

// terminatedBySignal records, per pid, the signal number this process used to
// TerminateProcess an external child, so the wait status can report the
// signal even if the child's exit code was overwritten (e.g. by the context
// kill racing the terminate). Entries are consumed by execWaitStatus.
var terminatedBySignal sync.Map // pid int -> num int

// stillActive is GetExitCodeProcess's "not exited yet" pseudo exit code
// (STATUS_PENDING). x/sys/windows does not export it.
const stillActive = 259

// errStopUnsupported is what the job-control signals report on a Windows
// whose ntdll does not export NtSuspendProcess/NtResumeProcess — the error
// text `kill` printed for every stop before suspend_windows.go existed.
var errStopUnsupported = errors.New("stop signals are not supported on this platform")

// sendSignal delivers sig to pid on Windows, where there is no kill(2):
//
//   - the shell's own pid is raised on the in-process bus, which is how a
//     background goroutine job's `kill -USR1 $$` reaches the parent's trap;
//   - signal 0 probes the process for existence (OpenProcess + exit code);
//   - a signal that may be caught is first offered to the target over its
//     bashy signal pipe (signalserver_windows.go); a sibling bashy runs its
//     trap or takes its default action from there. A missing pipe means the
//     target is not a bashy (or does not serve signals). KILL and the
//     job-control signals never take this route, see
//     windowsSignalBypassesPipe;
//   - otherwise the signal acts on the process itself: STOP/TSTP/TTIN/TTOU
//     suspend it and CONT resumes it through ntdll (suspend_windows.go),
//     CHLD/URG/WINCH are successful no-ops (their default action is not
//     death), and every remaining signal terminates the process with the
//     bashy signal marker as its exit code after recording the signal for
//     the wait status.
//
// GenerateConsoleCtrlEvent is deliberately not used: it cannot target a
// single process reliably (group 0 hits the whole console, another group id
// must be a process-group leader created with CREATE_NEW_PROCESS_GROUP).
func sendSignal(pid int, sig killSig) error {
	num := sig.Num
	if pid < 0 {
		// Windows has no process groups in the POSIX sense; treat a
		// group id as its leader, best effort.
		pid = -pid
	}
	if pid == os.Getpid() {
		if num != 0 {
			processSignalBus.raise(num)
		}
		return nil
	}
	if num == 0 {
		return probeProcess(pid)
	}
	if !windowsSignalBypassesPipe(num) {
		if err := sendSignalPipe(pid, num); err == nil {
			return nil
		} else if !errors.Is(err, errNoSignalPipe) {
			return err
		}
	}
	switch windowsSignalActionFor(num) {
	case windowsActionSuspend:
		return suspendProcess(pid)
	case windowsActionResume:
		return resumeProcess(pid)
	case windowsActionProbe:
		return probeProcess(pid)
	}
	return terminateProcess(pid, num)
}

// probeProcess implements signal 0: it succeeds if pid names a process that
// has not exited. The error text mirrors the errno strings bash prints.
func probeProcess(pid int) error {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return processError(err)
	}
	defer windows.CloseHandle(h)
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err != nil {
		return processError(err)
	}
	if code != stillActive {
		return syscall.ESRCH
	}
	return nil
}

// terminateProcess ends pid with the marker exit code for num and records the
// signal so execWaitStatus reports it even without the marker.
func terminateProcess(pid, num int) error {
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return processError(err)
	}
	defer windows.CloseHandle(h)
	if err := windows.TerminateProcess(h, encodeSignalMarker(num)); err != nil {
		var code uint32
		if qerr := windows.GetExitCodeProcess(h, &code); qerr == nil && code != stillActive {
			// Already gone: kill(2) reports ESRCH for a reaped process.
			return syscall.ESRCH
		}
		return processError(err)
	}
	terminatedBySignal.Store(pid, num)
	return nil
}

// processError maps the OpenProcess failures to the errno bash would print.
func processError(err error) error {
	switch {
	case errors.Is(err, windows.ERROR_INVALID_PARAMETER):
		return syscall.ESRCH // no such process
	case errors.Is(err, windows.ERROR_ACCESS_DENIED):
		return syscall.EPERM
	}
	return err
}

// errNoSignalPipe reports that the target serves no bashy signal pipe.
var errNoSignalPipe = errors.New("no bashy signal pipe")

// signalPipeBusyRetries bounds how long a sender waits for the target's pipe
// server to finish handling a concurrent message before giving up.
const (
	signalPipeBusyRetries = 50
	signalPipeBusyDelay   = 20 * time.Millisecond
)

// sendSignalPipe writes num to pid's bashy signal pipe. The server keeps one
// listening instance at a time, so a concurrent sender may see
// ERROR_PIPE_BUSY for the few microseconds a message takes; retry briefly.
func sendSignalPipe(pid, num int) error {
	name, err := windows.UTF16PtrFromString(signalPipeName(pid))
	if err != nil {
		return err
	}
	for attempt := 0; ; attempt++ {
		h, err := windows.CreateFile(name, windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, 0, 0)
		switch {
		case err == nil:
			defer windows.CloseHandle(h)
			var done uint32
			msg := encodeSignalMessage(num)
			if err := windows.WriteFile(h, msg, &done, nil); err != nil {
				return fmt.Errorf("signal pipe write: %w", err)
			}
			return nil
		case errors.Is(err, windows.ERROR_FILE_NOT_FOUND):
			return errNoSignalPipe
		case errors.Is(err, windows.ERROR_PIPE_BUSY) && attempt < signalPipeBusyRetries:
			time.Sleep(signalPipeBusyDelay)
			continue
		}
		return fmt.Errorf("signal pipe open: %w", err)
	}
}
