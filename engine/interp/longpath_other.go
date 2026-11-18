// Copyright (c) 2017, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

//go:build !linux

package interp

import (
	"io/fs"
	"os"
)

func statLongPath(path string, original error) (fs.FileInfo, error) {
	return nil, original
}

func accessLongFile(path string, mode uint32, original error) error {
	return original
}

func openLongExecPath(path string) (*os.File, bool, error) {
	return nil, false, nil
}

func physicalLongPath(path string, original error) (string, error) {
	return "", original
}
