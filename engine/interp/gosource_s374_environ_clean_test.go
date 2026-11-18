// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

//go:build full

package interp_test

import "testing"

// The program's environment is the one it was started with: no variable the
// interpreter uses to launch its dependency worker may appear in os.Environ
// (Go by Example, environment-variables).
func TestS374EnvironHidesWorkerBootstrap(t *testing.T) {
	differGoSource(t, `package main

import (
	"fmt"
	"os"
	"strings"
)

func main() {
	leaked := 0
	for _, e := range os.Environ() {
		name, _, _ := strings.Cut(e, "=")
		if strings.HasPrefix(name, "BASHPP_WORKER") {
			leaked++
		}
	}
	_, set := os.LookupEnv("BASHPP_WORKER_BOOTSTRAP")
	fmt.Println(leaked, set)
}
`, nil, "")
}
