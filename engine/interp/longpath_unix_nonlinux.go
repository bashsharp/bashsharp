// Copyright (c) 2017, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

//go:build unix && !linux

package interp

func accessLongPath(path string, mode uint32, original error) error {
	return original
}
