// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

import (
	"errors"
	"runtime"
	"testing"
)

// A fresh-stack hop must be invisible to the host code around it: the work
// runs to completion, a host panic keeps its value, and Goexit still ends the
// calling goroutine.
func TestBashPPOnFreshStack(t *testing.T) {
	ran := false
	bashPPOnFreshStack(func() { ran = true })
	if !ran {
		t.Fatal("fn did not run before the call returned")
	}

	want := errors.New("host failure")
	got := func() (p any) {
		defer func() { p = recover() }()
		bashPPOnFreshStack(func() { panic(want) })
		return nil
	}()
	if got != want {
		t.Fatalf("recovered %v, want the original panic value", got)
	}

	returned, exited := false, make(chan struct{})
	go func() {
		defer close(exited)
		bashPPOnFreshStack(runtime.Goexit)
		returned = true
	}()
	<-exited
	if returned {
		t.Fatal("the caller kept running after Goexit on the fresh stack")
	}
}
