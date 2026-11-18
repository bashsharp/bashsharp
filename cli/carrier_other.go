// Copyright (c) 2026 qiangli
// See LICENSE for licensing information

//go:build !unix && !windows

package cli

import "github.com/qiangli/bash/interp"

// platformJobCarrier reports that this platform has no OS-backed job carrier:
// it can neither re-exec this binary as a stand-in process nor recover the
// signal that ended one. Unix does both natively (carrier_unix.go) and Windows
// does both in its own dialect (carrier_windows.go); what is left here is
// plan9 and js/wasm. newRunner then keeps bash-mode background jobs on the
// synthetic g<N> handles and fails POSIX/sh mode closed via
// unsupportedJobCarrier — strict process semantics are never silently faked.
func platformJobCarrier() interp.JobCarrier { return nil }

func resetJobCarrierSignals() {}
func signalJobCarrierReady()  {}
