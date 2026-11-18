//go:build !darwin && !linux

// Copyright (c) 2017, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package cli

func currentStartupCredentials() (startupCredentialState, bool) {
	return startupCredentialState{}, false
}

var setStartupGID = func(int) error { return nil }
var setStartupUID = func(int) error { return nil }
