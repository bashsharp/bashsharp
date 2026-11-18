// Copyright (c) 2026, the outpost authors
// See LICENSE for licensing information

//go:build linux

package interp

import (
	"os"
	"os/signal"
	"syscall"
	"unsafe"
)

type signalDisposition [128]byte

func saveSignalDisposition(sig os.Signal) (signalDisposition, bool) {
	var disposition signalDisposition
	s, ok := sig.(syscall.Signal)
	if !ok {
		return disposition, false
	}
	_, _, errno := syscall.RawSyscall6(syscall.SYS_RT_SIGACTION,
		uintptr(s), 0, uintptr(unsafe.Pointer(&disposition[0])), linuxSigsetSize, 0, 0)
	return disposition, errno == 0
}

func restoreSignalDisposition(sig os.Signal, disposition signalDisposition) {
	s, ok := sig.(syscall.Signal)
	if !ok {
		return
	}
	_, _, _ = syscall.RawSyscall6(syscall.SYS_RT_SIGACTION,
		uintptr(s), uintptr(unsafe.Pointer(&disposition[0])), 0, linuxSigsetSize, 0, 0)
}

func restoreExecSignal(sig os.Signal) {
	s, ok := sig.(syscall.Signal)
	if !ok {
		signal.Reset(sig)
		return
	}
	var act [128]byte
	_, _, errno := syscall.RawSyscall6(syscall.SYS_RT_SIGACTION,
		uintptr(s), uintptr(unsafe.Pointer(&act[0])), 0, linuxSigsetSize, 0, 0)
	if errno != 0 {
		signal.Reset(sig)
	}
}

// setOSIgnore installs SIG_IGN for sig at the OS level via a raw rt_sigaction
// syscall, WITHOUT going through os/signal.Ignore. This is critical because
// signal.Ignore clears the Go runtime's internal "handling" bit, after which
// signal.Notify cannot re-enable delivery (VSC-PCTS TP714: trap ” SIG
// followed by trap 'action' SIG must catch the signal). By bypassing
// os/signal, the Go runtime's state is untouched; a later signal.Notify can
// override the OS-level SIG_IGN and re-install the Go handler atomically.
func setOSIgnore(sig os.Signal) {
	s, ok := sig.(syscall.Signal)
	if !ok {
		signal.Ignore(sig)
		return
	}
	var act [128]byte
	*(*uintptr)(unsafe.Pointer(&act[0])) = 1 // SIG_IGN
	_, _, errno := syscall.RawSyscall6(syscall.SYS_RT_SIGACTION,
		uintptr(s), uintptr(unsafe.Pointer(&act[0])), 0, linuxSigsetSize, 0, 0)
	if errno != 0 {
		signal.Ignore(sig)
	}
}

// osSignalIgnored reports whether the signal's real OS disposition is
// currently SIG_IGN, by probing sigaction without modifying it. Unlike
// signal.Ignored (which only tracks signals set via os/signal.Ignore), this
// detects inherited SIG_IGN dispositions from a parent process. Used by
// WithSignalResetter to avoid clearing an inherited SIG_IGN before
// startupIgnored can protect it (VSC-PCTS TP720).
func osSignalIgnored(sig os.Signal) bool {
	s, ok := sig.(syscall.Signal)
	if !ok {
		return signal.Ignored(sig)
	}
	var oldAct [128]byte
	_, _, errno := syscall.RawSyscall6(syscall.SYS_RT_SIGACTION,
		uintptr(s), 0, uintptr(unsafe.Pointer(&oldAct[0])), linuxSigsetSize, 0, 0)
	if errno != 0 {
		return false
	}
	return *(*uintptr)(unsafe.Pointer(&oldAct[0])) == 1 // SIG_IGN
}
