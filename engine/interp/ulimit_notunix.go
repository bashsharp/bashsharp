// Copyright (c) 2017, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

//go:build !unix

package interp

// nofileLimit reports the soft open-files limit (`ulimit -n`). Off unix
// there is no rlimit concept — report unlimited.
func nofileLimit() (cur uint64, ok bool) {
	return 0, false
}
