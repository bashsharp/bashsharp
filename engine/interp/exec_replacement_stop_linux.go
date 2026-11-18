// Copyright (c) 2026, the bashy authors
// See LICENSE for licensing information

//go:build linux

package interp

import (
	"os"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// linuxWaitidInfo is the stable 128-byte Linux siginfo_t layout for SIGCHLD.
// The child PID and wait status begin at offsets 16 and 24 on every Linux
// architecture; the surrounding union is deliberately opaque here.
type linuxWaitidInfo struct {
	Signo  int32
	Errno  int32
	Code   int32
	_      int32
	PID    int32
	UID    uint32
	Status int32
	_      [100]byte
}

const (
	linuxCLDStopped   = 5
	linuxCLDContinued = 6
)

// watchExecReplacementStops makes a proxied exec replacement preserve the
// process-boundary stop behavior of a real execve. cmd.Wait intentionally does
// not consume stopped states, so waitid can observe and consume only STOPPED /
// CONTINUED notifications without racing it for the terminal status.
func watchExecReplacementStops(pid int) func() {
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		pidfd, err := unix.PidfdOpen(pid, 0)
		if err != nil {
			pidfd = -1
		}
		if pidfd >= 0 {
			defer unix.Close(pidfd)
		}
		for {
			select {
			case <-done:
				return
			default:
			}
			var info linuxWaitidInfo
			_, _, errno := syscall.Syscall6(
				syscall.SYS_WAITID,
				uintptr(unix.P_PID), uintptr(pid), uintptr(unsafe.Pointer(&info)),
				uintptr(unix.WSTOPPED|unix.WCONTINUED|unix.WNOHANG), 0, 0,
			)
			if errno != 0 {
				if errno == syscall.EINTR {
					continue
				}
				return
			}
			if info.PID == 0 {
				time.Sleep(time.Millisecond)
				continue
			}
			switch info.Code {
			case linuxCLDStopped:
				sig := syscall.Signal(info.Status)
				if sig != syscall.SIGSTOP {
					restoreExecSignal(sig)
				}
				_ = syscall.Kill(os.Getpid(), sig)
				// Execution resumes here only after our parent continued the
				// proxy. Continue the represented child in the same breath.
				if pidfd >= 0 {
					_ = unix.PidfdSendSignal(pidfd, syscall.SIGCONT, nil, 0)
				} else {
					_ = syscall.Kill(pid, syscall.SIGCONT)
				}
			case linuxCLDContinued:
				// The continued state has now been consumed; keep observing.
			}
		}
	}()
	return func() {
		close(done)
		<-finished
	}
}
