// Copyright (c) 2026, the bashy authors
// See LICENSE for licensing information

//go:build !unix

package interp_test

import (
	"fmt"
	"os"
)

func unsupportedJobControlTestHelper() {
	fmt.Fprintln(os.Stderr, "POSIX process-group job control is unsupported")
	os.Exit(2)
}

func jobControlStopChild()             { unsupportedJobControlTestHelper() }
func jobControlTTYChild()              { unsupportedJobControlTestHelper() }
func jobControlReportShellForeground() { unsupportedJobControlTestHelper() }

func jobControlRunRedirectedShell() { unsupportedJobControlTestHelper() }
