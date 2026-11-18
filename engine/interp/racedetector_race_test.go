// Copyright (c) 2026, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

//go:build race

package interp

// raceEnabled reports whether this test binary was built with -race. The race
// detector costs roughly an order of magnitude in CPU, so a test that
// deliberately BURNS CPU to make a `time` report non-zero needs a proportionally
// longer deadline — otherwise it measures the harness, not the shell.
const raceEnabled = true
