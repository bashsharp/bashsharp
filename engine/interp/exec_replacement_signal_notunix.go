// Copyright (c) 2026, the bashy authors
// See LICENSE for licensing information

//go:build !unix && !windows

package interp

func forwardExecReplacementSignals(pid int) func() { return func() {} }

func forwardExecReplacementSignalsWithReport(pid int, report func(int)) func() {
	return forwardExecReplacementSignals(pid)
}

func forwardBashPPNativeSignalsWithReport(pid int, report func(int)) func() {
	return forwardExecReplacementSignalsWithReport(pid, report)
}
