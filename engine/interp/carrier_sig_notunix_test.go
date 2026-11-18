// Copyright (c) 2026, the bashy authors
// See LICENSE for licensing information

//go:build !unix

package interp_test

import "os"

// carrierExitSignal has no signal information to extract outside unix;
// carriers only ever report a normal exit. The carrier seam itself is
// portable — only signal relay fidelity is platform-bound.
func carrierExitSignal(ps *os.ProcessState) int {
	return 0
}
