// Copyright (c) 2017, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

//go:build unix && !linux && !darwin

package interp

func runnerPhysicalDir(r *Runner, fallback string) string {
	return fallback
}
