// Copyright (c) 2026, the bashy authors
// See LICENSE for licensing information

//go:build unix

package interp

import (
	"os"
	"os/signal"
	"syscall"
)

// forwardExecReplacementSignals preserves the parent-to-child half of an
// execve process boundary while the interpreter must proxy an exec replacement
// to keep live background jobs. Signals sent by the parent address the shell's
// PID, which a real execve would now belong to the replacement.
//
// SIGKILL and SIGSTOP cannot be caught and therefore remain an unavoidable
// proxy limitation. Signals already ignored at the OS boundary are skipped so
// the replacement retains execve's inherited SIG_IGN semantics.
func forwardExecReplacementSignals(pid int) func() {
	return forwardExecReplacementSignalsWithReport(pid, nil)
}

func forwardExecReplacementSignalsWithReport(pid int, report func(int)) func() {
	return forwardSignalsWithReport(pid, report, false)
}

// forwardBashPPNativeSignalsWithReport also subscribes to signals ignored by
// the process which launched the interpreter. The native Go helper is placed
// in its own process group for cleanup, so it would otherwise miss a signal
// sent to the interpreted program's group. A Go program may still explicitly
// subscribe to an inherited-ignored signal with os/signal.Notify.
func forwardBashPPNativeSignalsWithReport(pid int, report func(int)) func() {
	return forwardSignalsWithReport(pid, report, true)
}

func forwardSignalsWithReport(pid int, report func(int), includeIgnored bool) func() {
	ch := make(chan os.Signal, 16)
	var forwarded []os.Signal
	var dispositions []signalDisposition
	var ignored []bool
	for _, name := range [...]string{
		"HUP", "INT", "QUIT", "ABRT", "USR1", "USR2", "PIPE", "ALRM", "TERM",
		"TSTP", "TTIN", "TTOU", "XCPU", "XFSZ",
	} {
		sig, ok := signalByName(name)
		if !ok {
			continue
		}
		osSig := signalForOS(sig)
		wasIgnored := osSignalIgnored(osSig)
		if wasIgnored && !includeIgnored {
			continue
		}
		disposition, ok := saveSignalDisposition(osSig)
		if !ok {
			continue
		}
		forwarded = append(forwarded, osSig)
		dispositions = append(dispositions, disposition)
		ignored = append(ignored, wasIgnored)
	}
	if len(forwarded) == 0 {
		return func() {}
	}
	// OSSignalResetter may have installed SIG_DFL through raw sigaction after
	// clearing Go's handling bit. Synchronize os/signal's bookkeeping before
	// Notify so Linux reliably reinstalls the runtime trampoline.
	for i, sig := range forwarded {
		if !ignored[i] {
			signal.Reset(sig)
		}
	}
	signal.Notify(ch, forwarded...)
	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		for {
			select {
			case <-done:
				return
			case sig := <-ch:
				if unixSig, ok := sig.(syscall.Signal); ok {
					if syscall.Kill(pid, unixSig) == nil && report != nil {
						report(int(unixSig))
					}
				}
			}
		}
	}()
	return func() {
		signal.Stop(ch)
		close(done)
		<-finished
		for i, sig := range forwarded {
			restoreSignalDisposition(sig, dispositions[i])
		}
	}
}
