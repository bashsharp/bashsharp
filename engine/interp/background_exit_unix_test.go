// Copyright (c) 2026, the bashy authors
// See LICENSE for licensing information

//go:build unix

package interp_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestAsyncExternalSurvivesShellExit(t *testing.T) {
	for _, tc := range []struct {
		name   string
		script func(string) string
	}{
		{"delayed", func(out string) string { return fmt.Sprintf("/bin/sh -c 'sleep 1; : > %q' &", out) }},
		{"fast", func(out string) string { return fmt.Sprintf("/usr/bin/touch %q &", out) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "out")
			file, err := syntax.NewParser().Parse(strings.NewReader(tc.script(out)), "")
			if err != nil {
				t.Fatal(err)
			}
			runner, err := interp.New()
			if err != nil {
				t.Fatal(err)
			}
			if err := runner.Run(context.Background(), file); err != nil {
				t.Fatal(err)
			}
			// Reset models the runner's teardown after a complete shell file.
			runner.Reset()
			deadline := time.Now().Add(5 * time.Second)
			for {
				if _, err := os.Stat(out); err == nil {
					return
				} else if !os.IsNotExist(err) {
					t.Fatal(err)
				}
				if time.Now().After(deadline) {
					t.Fatalf("background command did not create %s", out)
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}

func TestRunFileDoesNotWaitForExternalBackgroundCompletion(t *testing.T) {
	for _, src := range []string{
		"/bin/sleep 1 &",
		"echo() { /bin/sleep 1; }; echo &",
		// Expansion is the child's work, not part of its launch.
		"/bin/echo $(/bin/sleep 1) >/dev/null &",
	} {
		file, err := syntax.NewParser().Parse(strings.NewReader(src), "")
		if err != nil {
			t.Fatal(err)
		}
		runner, err := interp.New()
		if err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		if err := runner.Run(context.Background(), file); err != nil {
			t.Fatal(err)
		}
		if elapsed := time.Since(start); elapsed >= 500*time.Millisecond {
			t.Fatalf("file run waited for %q background completion: %v", src, elapsed)
		}
	}
}

// A shell that exits right after `cmd &` leaves no time for a command that was
// only scheduled: the process must exist when the statement returns.
func TestAsyncExternalStartedBeforeStatementReturns(t *testing.T) {
	file, err := syntax.NewParser().Parse(strings.NewReader("/bin/sleep 30 &"), "")
	if err != nil {
		t.Fatal(err)
	}
	var pid atomic.Int64
	runner, err := interp.New(interp.WithBgPidCallback(func(p int) { pid.Store(int64(p)) }))
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background(), file); err != nil {
		t.Fatal(err)
	}
	started := int(pid.Load())
	if started <= 0 {
		t.Fatal("background command was not started when its statement returned")
	}
	if err := syscall.Kill(started, syscall.SIGKILL); err != nil {
		t.Fatalf("background command %d is not running: %v", started, err)
	}
}

// The launch rendezvous must hand back a job whose redirection waits for a
// peer that only the parent's next statement provides.
func TestAsyncExternalLaunchYieldsToBlockingOpen(t *testing.T) {
	dir := t.TempDir()
	fifo, out := filepath.Join(dir, "fifo"), filepath.Join(dir, "out")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	src := fmt.Sprintf("/bin/cat <%q >%q & echo hi >%q; wait", fifo, out, fifo)
	file, err := syntax.NewParser().Parse(strings.NewReader(src), "")
	if err != nil {
		t.Fatal(err)
	}
	runner, err := interp.New()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := runner.Run(ctx, file); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hi\n" {
		t.Fatalf("got %q, want %q", got, "hi\n")
	}
}
