// Copyright (c) 2026, the outpost authors
// See LICENSE for licensing information

//go:build linux

package interp

import (
	"errors"
	"io"
	"os"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

const linuxKernelSigsetSize = unsafe.Sizeof(uint64(0))

func linuxSiginfoSenderPID(info *unix.Siginfo) int32 {
	return *(*int32)(unsafe.Add(unsafe.Pointer(info), linuxSiginfoPIDOffset))
}

// writePipelineOutput writes a builtin's output without allowing the
// writer-side SIGPIPE to escape the goroutine-based pipeline subshell. Linux
// directs this SIGPIPE to the calling thread, so block it on a locked thread,
// inspect the pending signal after the write, requeue distinguishable explicit
// deliveries, and restore the exact prior mask. A signal pending before entry
// or a previously blocked SIGPIPE is never consumed. See the classifier below
// for Linux's unavoidable same-thread SI_USER coalescing limit.
func writePipelineOutput(w io.Writer, p []byte) (int, error) {
	return writePipelineOutputAfterSnapshot(w, p, nil)
}

func writePipelineOutputAfterSnapshot(w io.Writer, p []byte, afterSnapshot func()) (int, error) {
	f, ok := w.(*os.File)
	if !ok {
		return w.Write(p)
	}

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	mask := uint64(1) << (uint(syscall.SIGPIPE) - 1)
	var oldMask uint64
	if errno := rawSigprocmask(unix.SIG_BLOCK, &mask, &oldMask); errno != 0 {
		return f.Write(p)
	}
	defer rawSigprocmask(unix.SIG_SETMASK, &oldMask, nil)

	var pendingBefore uint64
	_, _, _ = syscall.RawSyscall(
		syscall.SYS_RT_SIGPENDING,
		uintptr(unsafe.Pointer(&pendingBefore)),
		linuxKernelSigsetSize,
		0,
	)
	if afterSnapshot != nil {
		afterSnapshot()
	}

	n, err := f.Write(p)
	if oldMask&mask == 0 && pendingBefore&mask == 0 {
		// A zero timeout makes rt_sigtimedwait a nonblocking consume. Classify
		// its siginfo before deciding whether to absorb or requeue it.
		var info unix.Siginfo
		var timeout syscall.Timespec
		sig, _, _ := syscall.RawSyscall6(
			syscall.SYS_RT_SIGTIMEDWAIT,
			uintptr(unsafe.Pointer(&mask)),
			uintptr(unsafe.Pointer(&info)),
			uintptr(unsafe.Pointer(&timeout)),
			linuxKernelSigsetSize,
			0,
			0,
		)
		writeGenerated := linuxWriteGeneratedSIGPIPE(&info, n, len(p), err)
		if sig == uintptr(syscall.SIGPIPE) && writeGenerated {
			// A pipe write may return a positive partial count rather than
			// EPIPE after the reader disappears, but Bash still terminates
			// the pipeline child for the generated SIGPIPE.
			err = syscall.EPIPE
		} else if sig == uintptr(syscall.SIGPIPE) {
			// An independently sent signal won the pending slot. Put it back
			// on this still-blocked thread; restoring oldMask below delivers
			// it through the shell's unchanged disposition.
			_, _, _ = syscall.RawSyscall(
				syscall.SYS_TGKILL,
				uintptr(os.Getpid()),
				uintptr(unix.Gettid()),
				uintptr(syscall.SIGPIPE),
			)
		}
	}
	return n, err
}

func linuxWriteGeneratedSIGPIPE(info *unix.Siginfo, written, wanted int, writeErr error) bool {
	if info.Code != 0 {
		return false
	}
	sender := linuxSiginfoSenderPID(info)
	if sender == 0 {
		return true
	}
	// Linux can materialize pipe-write SEND_SIG_NOINFO as SI_USER from the
	// current process. That is byte-for-byte identical to a same-thread
	// kill(2) once standard SIGPIPE instances coalesce. When the write itself
	// proves pipe failure, preserve Bash pipeline semantics and consume it.
	// Distinguishable explicit sources (another PID or SI_TKILL) are requeued;
	// the same-thread SI_USER collision is a kernel-information limit.
	return sender == int32(os.Getpid()) &&
		(errors.Is(writeErr, syscall.EPIPE) || written < wanted)
}

func rawSigprocmask(how int, set, old *uint64) syscall.Errno {
	_, _, errno := syscall.RawSyscall6(
		syscall.SYS_RT_SIGPROCMASK,
		uintptr(how),
		uintptr(unsafe.Pointer(set)),
		uintptr(unsafe.Pointer(old)),
		linuxKernelSigsetSize,
		0,
		0,
	)
	return errno
}
