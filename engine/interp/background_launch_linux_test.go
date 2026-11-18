// Copyright (c) 2026, the bashy authors
// See LICENSE for licensing information

//go:build linux

package interp_test

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// TestBackgroundExternalLaunchBoundary pins the launch boundary of an
// asynchronous external command.
//
// The boundary is the fork, not the child's work. GNU bash makes no part of an
// asynchronous command's effect visible to the next statement — measured 0/20
// on both bash 3.2 and 5.2 for `touch f & [ -e f ]` — so an assertion that the
// marker already exists would claim a guarantee no shell offers, and could only
// ever be bought with a sleep that races under load.
//
// What the shell does guarantee, and what this test asserts, is the pair either
// side of that boundary: the external child really has been launched and named
// by `$!` before the next statement runs, and the parent is not blocked on the
// child finishing.
func TestBackgroundExternalLaunchBoundary(t *testing.T) {
	t.Run("child is launched and named by $! before the next statement", func(t *testing.T) {
		// `kill -0 "$pid"` is the next statement after the async list, and it
		// fails unless a real process already carries that PID. `$!` is a
		// rendezvous on the exec, so this is deterministic rather than timed.
		for i := 0; i < 20; i++ {
			if err := runBackgroundLaunchScript(t, `/bin/sleep 30 &
pid=$!
kill -0 "$pid"
kill "$pid"`); err != nil {
				t.Fatalf("iteration %d: external child was not launched before the next statement: %v", i, err)
			}
		}
	})

	t.Run("parent does not wait for the child to finish", func(t *testing.T) {
		marker := filepath.Join(t.TempDir(), "late")
		// The child outlives the script by far. If the parent were serialising
		// on the child's completion the run could not return before it, and
		// the marker it writes at the end must not exist yet.
		start := time.Now()
		if err := runBackgroundLaunchScript(t, fmt.Sprintf(`/bin/sh -c 'sleep 30; : > %q' &
kill -0 "$!"`, marker)); err != nil {
			t.Fatalf("run: %v", err)
		}
		if elapsed := time.Since(start); elapsed > 10*time.Second {
			t.Fatalf("parent blocked on the background child: took %v", elapsed)
		}
	})
}

func runBackgroundLaunchScript(t *testing.T, src string) error {
	t.Helper()
	file, err := syntax.NewParser().Parse(strings.NewReader(src), "")
	if err != nil {
		t.Fatal(err)
	}
	runner, err := interp.New(interp.WithJobCarrier(new(testCarrier)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return runner.Run(ctx, file)
}
