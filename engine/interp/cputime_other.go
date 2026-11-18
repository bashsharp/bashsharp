// Copyright (c) 2024, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

//go:build !unix && !windows

package interp

import "time"

// processCPUTimes is unavailable on targets that expose neither
// getrusage nor GetProcessTimes (e.g. plan9, js/wasm). It fails closed —
// ok is false — so the `time` keyword reports whatever external-child CPU
// was gathered rather than fabricating a shell-process figure it cannot
// verify.
func processCPUTimes() (user, sys time.Duration, ok bool) {
	return 0, 0, false
}
