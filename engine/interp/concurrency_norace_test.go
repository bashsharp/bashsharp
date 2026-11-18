// Copyright (c) 2026, the bashy authors
// See LICENSE for licensing information

//go:build !race

package interp_test

// cgRaceEnabled reports whether this external test binary was built with -race.
// See concurrency_race_test.go for why interp_test carries its own flag.
const cgRaceEnabled = false
