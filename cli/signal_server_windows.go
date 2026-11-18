// Copyright (c) 2026 qiangli
// See LICENSE for licensing information

//go:build windows

package cli

import (
	"os"

	"github.com/qiangli/bash/interp"
)

// startProcessSignalServer makes this standalone shell signallable by a
// sibling bashy's `kill` (Sprint 245): Windows has no kill(2), so bashy
// processes reach each other over a per-process named pipe served by the
// engine (interp.StartProcessSignalServer), and a signal with neither trap
// nor ignore exits with the engine's marker code so a parent bashy reports
// the death as 128+signal ("Terminated"), as bash does. A failure to open
// the pipe is not fatal: `kill` from a sibling then falls back to
// TerminateProcess, which still carries the marker.
func startProcessSignalServer() (stop func()) {
	interp.SetProcessSignalDefault(func(num int) {
		os.Exit(interp.SignalMarkerExitCode(num))
	})
	stop, err := interp.StartProcessSignalServer()
	if err != nil || stop == nil {
		return func() {}
	}
	return stop
}
