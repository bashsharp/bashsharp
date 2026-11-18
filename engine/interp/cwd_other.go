// Copyright (c) 2017, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

//go:build !unix

package interp

import "os"

func currentWorkingDir() (string, error) { return os.Getwd() }

func openRunnerDir(path string) (*os.File, error) {
	return nil, nil
}

func openRunnerDirAt(file *os.File, path string) (*os.File, bool, error) {
	return nil, false, nil
}

func dupRunnerDir(file *os.File) (*os.File, error) {
	return nil, nil
}

func runnerExecDir(r *Runner, fallback string) string {
	return fallback
}

func runnerPhysicalDir(r *Runner, fallback string) string {
	return fallback
}
