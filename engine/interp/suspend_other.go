// Copyright (c) 2026, the outpost authors
// See LICENSE for licensing information

//go:build !windows

package interp

import "errors"

// errStopUnsupported is the error a platform without a process-suspension
// primitive reports for STOP/TSTP/TTIN/TTOU. Unix never reaches it — the
// kernel delivers the real signal — and plan9 has nothing to offer.
var errStopUnsupported = errors.New("stop signals are not supported on this platform")

// suspendProcess and resumeProcess are the Windows job-control primitives
// (suspend_windows.go). Off Windows nothing calls them; the declarations
// exist so the shared job-control code compiles everywhere.
func suspendProcess(pid int) error { return errStopUnsupported }

func resumeProcess(pid int) error { return errStopUnsupported }
