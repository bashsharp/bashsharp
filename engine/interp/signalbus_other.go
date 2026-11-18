// Copyright (c) 2026, the outpost authors
// See LICENSE for licensing information

//go:build !windows

package interp

import "os"

// Off Windows the kernel delivers signals; the bus seams are no-ops.
const signalBusActive = false

func busNotify(chan<- os.Signal, os.Signal) {}
func busStop(chan<- os.Signal)              {}
func busIgnore(os.Signal)                   {}
func busReset(os.Signal)                    {}
