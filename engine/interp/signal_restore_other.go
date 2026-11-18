// Copyright (c) 2026, the outpost authors
// See LICENSE for licensing information

//go:build !darwin && !linux

package interp

import (
	"os"
	"os/signal"
)

type signalDisposition struct{}

func saveSignalDisposition(os.Signal) (signalDisposition, bool) { return signalDisposition{}, false }
func restoreSignalDisposition(os.Signal, signalDisposition)     {}

func restoreExecSignal(sig os.Signal) {
	signal.Reset(sig)
}

// setOSIgnore installs SIG_IGN via os/signal on platforms without raw
// sigaction access. signal.Notify may not re-enable delivery after this;
// TP714 is best-effort on unsupported platforms (see linux/darwin builds).
func setOSIgnore(sig os.Signal) {
	signal.Ignore(sig)
}

// osSignalIgnored checks Go's signal state on unsupported platforms.
func osSignalIgnored(sig os.Signal) bool {
	return signal.Ignored(sig)
}
