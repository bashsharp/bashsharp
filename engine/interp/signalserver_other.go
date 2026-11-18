// Copyright (c) 2026, the outpost authors
// See LICENSE for licensing information

//go:build !windows

package interp

// StartProcessSignalServer is a no-op off Windows, where the kernel delivers
// signals; see signalserver_windows.go for the contract.
func StartProcessSignalServer() (stop func(), err error) {
	return func() {}, nil
}
