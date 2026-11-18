// Copyright (c) 2026, the outpost authors
// See LICENSE for licensing information

//go:build windows

package interp

import (
	"syscall"

	"golang.org/x/sys/windows"
)

// Windows has no SIGSTOP, and no documented API suspends another process as
// a whole: SuspendThread works per thread, and enumerating a live process's
// threads races with it creating more. The primitives every process viewer
// (Process Explorer, Process Hacker, pssuspend) actually uses are
// NtSuspendProcess and NtResumeProcess in ntdll.dll, which take a single
// process handle opened for PROCESS_SUSPEND_RESUME and stop or restart every
// thread atomically.
//
// Both are UNDOCUMENTED: Microsoft publishes no header, no import library
// and no compatibility promise for them. They have nonetheless been exported
// by ntdll since Windows XP, across every Windows this shell supports, and
// the suspend count they manipulate is the same one SuspendThread uses. We
// therefore resolve them lazily and treat a missing export as "this platform
// cannot stop a process", which is exactly the error the kill builtin
// printed before this existed — a Windows without them degrades, it does not
// fail to start.
var (
	ntdll                = windows.NewLazySystemDLL("ntdll.dll")
	procNtSuspendProcess = ntdll.NewProc("NtSuspendProcess")
	procNtResumeProcess  = ntdll.NewProc("NtResumeProcess")
)

// suspendProcess stops every thread of pid: the Windows stand-in for
// delivering SIGSTOP/SIGTSTP to another process. The process stays alive and
// waitable — a parent blocked in cmd.Wait() keeps waiting, and
// TerminateProcess still works on it, so `kill -KILL` on a stopped job
// behaves as it does on Unix.
func suspendProcess(pid int) error {
	return ntProcessControl(pid, procNtSuspendProcess)
}

// resumeProcess restarts a process stopped by [suspendProcess]: the stand-in
// for SIGCONT. Resuming a process that is not suspended is a successful
// no-op, matching SIGCONT aimed at a running process.
func resumeProcess(pid int) error {
	return ntProcessControl(pid, procNtResumeProcess)
}

// ntProcessControl opens pid for suspend/resume and calls proc on the
// handle. Errors are mapped to the errno strings bash prints for kill(2), so
// `kill -STOP` on a dead or inaccessible pid reads the same as on Unix.
func ntProcessControl(pid int, proc *windows.LazyProc) error {
	if err := proc.Find(); err != nil {
		return errStopUnsupported
	}
	h, err := windows.OpenProcess(windows.PROCESS_SUSPEND_RESUME|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return processError(err)
	}
	defer windows.CloseHandle(h)
	// A handle to an exited process stays valid until every reference is
	// dropped, and ntdll would happily "suspend" the corpse; kill(2)
	// reports ESRCH for a process that is already gone.
	var code uint32
	if err := windows.GetExitCodeProcess(h, &code); err == nil && code != stillActive {
		return syscall.ESRCH
	}
	status, _, _ := proc.Call(uintptr(h))
	if st := windows.NTStatus(uint32(status)); !ntSuccess(st) {
		return st.Errno()
	}
	return nil
}

// ntSuccess is the NT_SUCCESS macro: an NTSTATUS is a failure only when its
// severity bits make it negative. Success and informational codes (which
// NtResumeProcess may return) are not errors.
func ntSuccess(status windows.NTStatus) bool {
	return int32(status) >= 0
}
