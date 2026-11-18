// Copyright (c) 2026, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

//go:build !race

package interp

// raceEnabled reports whether this test binary was built with -race.
const raceEnabled = false
