// Copyright (c) 2026, the outpost authors
// See LICENSE for licensing information

//go:build darwin

package interp

import (
	"os"
	"os/signal"
	"syscall"
	"unsafe"
)

// signalDisposition matches Darwin's public struct sigaction, which is the
// shape accepted by libc sigaction. libc supplies the private kernel
// trampoline field that a direct SYS_SIGACTION call cannot safely construct.
type signalDisposition struct {
	handler uintptr
	mask    uint32
	flags   int32
}

// syscallPtr is the standard library's supported Darwin libc-call boundary.
// Unlike a direct assembly CALL, it moves onto the runtime-managed system
// stack and informs the scheduler around the blocking foreign call.
//
//go:linkname syscallPtr syscall.syscallPtr
func syscallPtr(fn, a1, a2, a3 uintptr) (r1, r2 uintptr, err syscall.Errno)

// sigactionTrampolineAddr returns an ABI0 assembly trampoline. syscallPtr
// invokes that trampoline through runtime.libcCall; the trampoline then calls
// libc sigaction using the platform C ABI.
func sigactionTrampolineAddr() uintptr

// sigactionTrampoline is the assembly trampoline itself. It is reached only
// through runtime.libcCall, never called from Go; the declaration exists so
// the symbol has a Go-visible signature (as the standard library declares its
// libc_*_trampoline stubs) and vet's asmdecl can check the assembly against
// it.
func sigactionTrampoline()

//go:cgo_import_dynamic libc_sigaction sigaction "/usr/lib/libSystem.B.dylib"

func libcSigaction(sig uint32, new, old *signalDisposition) int32 {
	r1, _, _ := syscallPtr(sigactionTrampolineAddr(), uintptr(sig),
		uintptr(unsafe.Pointer(new)), uintptr(unsafe.Pointer(old)))
	return int32(r1)
}

func saveSignalDisposition(sig os.Signal) (signalDisposition, bool) {
	var disposition signalDisposition
	s, ok := sig.(syscall.Signal)
	if !ok {
		return disposition, false
	}
	return disposition, libcSigaction(uint32(s), nil, &disposition) == 0
}

func restoreSignalDisposition(sig os.Signal, disposition signalDisposition) {
	if s, ok := sig.(syscall.Signal); ok {
		libcSigaction(uint32(s), &disposition, nil)
	}
}

func restoreExecSignal(sig os.Signal) {
	s, ok := sig.(syscall.Signal)
	if !ok || libcSigaction(uint32(s), &signalDisposition{}, nil) != 0 {
		signal.Reset(sig)
	}
}

// setOSIgnore installs SIG_IGN for sig at the OS level without changing the
// os/signal package's bookkeeping. This is required for ignored-on-entry
// synchronous fault signals: a kill-delivered fault must be discarded while
// a genuine hardware fault remains owned by the Go runtime.
func setOSIgnore(sig os.Signal) {
	s, ok := sig.(syscall.Signal)
	if !ok || libcSigaction(uint32(s), &signalDisposition{handler: 1}, nil) != 0 {
		signal.Ignore(sig)
	}
}

// osSignalIgnored reports the real Darwin disposition, including SIG_IGN
// inherited across exec before os/signal has updated its bookkeeping.
func osSignalIgnored(sig os.Signal) bool {
	disposition, ok := saveSignalDisposition(sig)
	return ok && disposition.handler == 1
}
