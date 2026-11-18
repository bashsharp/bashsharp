// Copyright (c) 2026, the bashy authors
// See LICENSE for licensing information

//go:build !unix

package interp_test

import "fmt"

// On non-unix targets (plan9, js/wasm, windows) there is no portable per-process
// open-fd directory to scan, so fd-leak detection is unavailable. Returning an
// error makes cgLeakDetector skip the fd check on those platforms rather than
// report a spurious leak; the goroutine leak check still runs everywhere.
var errNoFDIntrospection = fmt.Errorf("fd introspection unavailable on this platform")

func cgCountFDs() (int, error) { return 0, errNoFDIntrospection }

func cgListFDs() ([]string, error) { return nil, errNoFDIntrospection }
