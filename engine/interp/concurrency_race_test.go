// Copyright (c) 2026, the bashy authors
// See LICENSE for licensing information

//go:build race

package interp_test

// cgRaceEnabled reports whether this external test binary was built with -race.
// The internal interp package has its own raceEnabled; interp_test cannot see
// it, so the concurrency gate carries its own copy. The race detector costs
// roughly an order of magnitude in CPU, so the schedule matrix trims repetition
// under -race + -short to stay within a sane wall-clock.
const cgRaceEnabled = true
