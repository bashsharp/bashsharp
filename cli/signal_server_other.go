// Copyright (c) 2026 qiangli
// See LICENSE for licensing information

//go:build !windows

package cli

// startProcessSignalServer is a no-op off Windows: the kernel delivers
// signals there. See signal_server_windows.go.
func startProcessSignalServer() (stop func()) { return func() {} }
