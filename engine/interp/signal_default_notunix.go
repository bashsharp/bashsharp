// Copyright (c) 2026, the outpost authors
// See LICENSE for licensing information

//go:build !unix && !windows

package interp

import "os"

func relayRuntimeDefaultSignal(os.Signal) bool { return false }
