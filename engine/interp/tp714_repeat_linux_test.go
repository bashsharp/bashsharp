// Copyright (c) 2026, the outpost authors
// See LICENSE for licensing information

package interp

import (
	"bufio"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// TestTP714RuntimeSignalRepeatsAfterIgnore exercises the Profile D TP714
// sequence through a process boundary. After an ignored SIGSEGV, each of two
// separately delivered SIGSEGV instances must interrupt the same blocked read
// and run its action; a normal input line then completes that read.
func TestTP714RuntimeSignalRepeatsAfterIgnore(t *testing.T) {
	const helperEnv = "SH_TP714_REPEAT_HELPER"
	if os.Getenv(helperEnv) != "" {
		src := `trap '' SEGV
echo ignore-ready
read ignored
echo still running after ignoring SEGV
trap 'echo SEGV received' SEGV
echo trap-ready
read input
echo successfully read input
`
		file, err := syntax.NewParser().Parse(strings.NewReader(src), "")
		if err != nil {
			t.Fatal(err)
		}
		r, err := New(
			StdIO(os.Stdin, os.Stdout, os.Stderr),
			WithSignalResetter(OSSignalResetter{}),
			WithStandaloneSignalDefaults(),
		)
		if err != nil {
			t.Fatal(err)
		}
		if err := r.Run(t.Context(), file); err != nil {
			t.Fatal(err)
		}
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestTP714RuntimeSignalRepeatsAfterIgnore$")
	cmd.Env = append(os.Environ(), "GOSH_PROG=", helperEnv+"=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder // bashpp-racegate:safe-private
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line := bufio.NewReader(stdout)
	wantLine := func(want string) {
		t.Helper()
		gotc := make(chan struct {
			line string
			err  error
		}, 1)
		go func() {
			got, err := line.ReadString('\n')
			gotc <- struct {
				line string
				err  error
			}{got, err}
		}()
		select {
		case got := <-gotc:
			if got.err != nil || got.line != want+"\n" {
				t.Fatalf("line = %q, %v; want %q (stderr %q)", got.line, got.err, want, stderr.String())
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for %q (stderr %q)", want, stderr.String())
		}
	}

	wantLine("ignore-ready")
	if err := cmd.Process.Signal(syscall.SIGSEGV); err != nil {
		t.Fatal(err)
	}
	if _, err := stdin.Write([]byte("ignored\n")); err != nil {
		t.Fatal(err)
	}
	wantLine("still running after ignoring SEGV")
	wantLine("trap-ready")
	for range 2 {
		if err := cmd.Process.Signal(syscall.SIGSEGV); err != nil {
			t.Fatal(err)
		}
		wantLine("SEGV received")
	}
	if _, err := stdin.Write([]byte("input\n")); err != nil {
		t.Fatal(err)
	}
	if err := stdin.Close(); err != nil {
		t.Fatal(err)
	}
	wantLine("successfully read input")
	if err := cmd.Wait(); err != nil {
		t.Fatalf("helper: %v; stderr %q", err, stderr.String())
	}
}
