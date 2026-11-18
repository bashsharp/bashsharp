// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

//go:build full

package interp_test

import "testing"

// A NaN that a dependency writes back inside a slice keeps its bits, as in
// test/typeparam/ordered.go: sort.Float64s reorders a slice holding NaNs.
func TestS374NaNSurvivesNativeSliceWriteback(t *testing.T) {
	differGoSource(t, `package main

import (
	"fmt"
	"math"
	"sort"
)

func main() {
	s := []float64{74.3, math.Inf(1), -784.0, math.NaN(), 2.3, math.NaN(), math.Inf(-1)}
	sort.Float64s(s)
	nans := 0
	for _, f := range s {
		if f != f {
			nans++
		}
	}
	fmt.Println(len(s), nans, s[2:])
}
`, nil, "")
}
