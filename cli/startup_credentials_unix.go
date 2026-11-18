//go:build darwin || linux

// Copyright (c) 2017, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package cli

import "syscall"

func currentStartupCredentials() (startupCredentialState, bool) {
	return startupCredentialState{
		realUID:      syscall.Getuid(),
		effectiveUID: syscall.Geteuid(),
		realGID:      syscall.Getgid(),
		effectiveGID: syscall.Getegid(),
	}, true
}

var setStartupGID = syscall.Setgid
var setStartupUID = syscall.Setuid
